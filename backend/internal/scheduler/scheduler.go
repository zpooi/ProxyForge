package scheduler

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zpooi/ProxyForge/backend/internal/db"
	"github.com/zpooi/ProxyForge/backend/internal/models"
	"github.com/zpooi/ProxyForge/backend/internal/proxy"
	"github.com/zpooi/ProxyForge/backend/internal/test"
	"github.com/zpooi/ProxyForge/backend/internal/warp"
)

const (
	minWarpPoolReserve     = 3
	maxAutoRegisterBatch   = 50
	minHealthySpeedBps     = 120 * 1024
	maxHealthyLatencyMs    = 700
	maxHealthyPacketLoss   = 0.50
	untestedAccountGrace   = 3 * time.Minute
	// 探测间隔拉长到 60s，配合更高的重绑阈值降低抖动。免费 WARP 出口 IP
	// 本来就会自然漂移，频繁探测 + 低阈值会让 IP 一直变、节点被反复换掉。
	slotProbeInterval  = 60 * time.Second
	slotProbeTimeout   = 12 * time.Second
	slotProbeFailure   = "实时探测失败: "
	slotIPDriftFailure = "出口 IP 漂移: "
	// 重绑/漂移阈值从 5 提到 8，给"原地重启隧道"更多机会先恢复，
	// 换绑到新账号（会改变出口 IP）成为最后手段。
	slotProbeRebindAfter   = 8
	slotIPDriftRebindAfter = 8
	// 隧道健康检查间隔：持续拨号失败的隧道由 HealthCheck 原地重建。
	healthCheckInterval = 20 * time.Second
)

type Scheduler struct {
	db      *db.DB
	manager *proxy.Manager
	warp    *warp.Client

	mu       sync.Mutex
	running  bool
	manualCh chan string

	lastRunAt          time.Time
	refillBackoffUntil time.Time
}

func New(database *db.DB, manager *proxy.Manager, warpClient *warp.Client) *Scheduler {
	return &Scheduler{
		db:       database,
		manager:  manager,
		warp:     warpClient,
		manualCh: make(chan string, 8),
	}
}

func (s *Scheduler) Run(ctx context.Context) {
	go s.trafficLoop(ctx)
	go s.autoRefillLoop(ctx)
	go s.slotProbeLoop(ctx)
	go s.healthCheckLoop(ctx)

	for {
		interval := s.dedupInterval()
		if interval < 60 {
			interval = 600
		}
		timer := time.NewTimer(time.Until(s.nextRunTime(interval)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case kind := <-s.manualCh:
			timer.Stop()
			if s.execute(ctx, kind) {
				s.lastRunAt = time.Now()
			}
		case <-timer.C:
			if s.execute(ctx, "dedup") {
				s.lastRunAt = time.Now()
			}
		}
	}
}

func (s *Scheduler) autoRefillLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(45 * time.Second):
	}

	ticker := time.NewTicker(90 * time.Second)
	defer ticker.Stop()
	for {
		s.autoRefill(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// healthCheckLoop 定期让 Manager 原地重建持续拨号失败的隧道。这是节点自愈
// 的快速通路：不换账号、不改出口 IP，只把断掉的隧道重新拉起来。
func (s *Scheduler) healthCheckLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(20 * time.Second):
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		if rebuilt := s.manager.HealthCheck(); rebuilt > 0 {
			log.Printf("[scheduler] health check rebuilt %d tunnel(s)", rebuilt)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) slotProbeLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(25 * time.Second):
	}

	ticker := time.NewTicker(slotProbeInterval)
	defer ticker.Stop()
	for {
		s.probeProxySlots(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Scheduler) probeProxySlots(ctx context.Context) {
	if !s.tryStartWork() {
		return
	}
	defer s.finishWork()

	if changed, _, err := s.rebindProxySlots(); err != nil {
		log.Printf("[scheduler] realtime probe pre-bind: %v", err)
	} else if changed > 0 {
		log.Printf("[scheduler] realtime probe pre-bound %d slot(s)", changed)
		if err := s.reconcileManager(); err != nil {
			log.Printf("[scheduler] realtime probe reconcile after pre-bind: %v", err)
		}
	}

	slots, err := s.db.ListProxySlots()
	if err != nil {
		log.Printf("[scheduler] realtime probe list slots: %v", err)
		return
	}
	password, _, _ := s.db.GetSetting(db.SettingProxyPassword)
	tester := test.NewTester(s.proxyPort(), password)
	running := s.runningTagSet()

	checked := 0
	failures := 0
	needsReconcile := false
	for _, slot := range slots {
		if slot.Status != "active" || slot.AccountID == nil || slot.AccountTag == "" || slot.AccountStatus != "active" {
			continue
		}
		checked++
		if !running[slot.AccountTag] {
			_ = s.db.MarkProxySlotProbeFailure(slot.ID, formatSlotProbeFailure(slot.ProbeFailures+1, "WARP 隧道未运行"))
			failures++
			continue
		}

		probeCtx, cancel := context.WithTimeout(ctx, slotProbeTimeout)
		result := tester.ProbeProxy(probeCtx, slot.AccountTag, password)
		cancel()
		if result.Err != nil {
			_ = s.db.MarkProxySlotProbeFailure(slot.ID, formatSlotProbeFailure(slot.ProbeFailures+1, result.Err.Error()))
			failures++
			continue
		}
		acceptedIP, restartTunnel := s.acceptSlotPublicIP(slot, result.PublicIP)
		if restartTunnel && s.manager.StopTunnel(slot.AccountTag) {
			needsReconcile = true
			log.Printf("[scheduler] stopped drifted tunnel %s to recover stable IP", slot.AccountTag)
		}
		if !acceptedIP {
			failures++
			continue
		}
		if err := s.db.UpdateAccountRealtimeProbe(*slot.AccountID, result.PublicIP, result.Colo, result.Country, result.LatencyMs); err != nil {
			log.Printf("[scheduler] realtime probe update %s: %v", slot.AccountTag, err)
		}
		if slot.LastError != "" || slot.ProbeFailures > 0 || slot.IPDriftFailures > 0 {
			_ = s.db.MarkProxySlotError(slot.ID, "")
		}
	}

	if needsReconcile {
		if err := s.reconcileManager(); err != nil {
			log.Printf("[scheduler] realtime probe reconcile after IP drift restart: %v", err)
		}
	}

	if failures > 0 {
		changed, _, err := s.rebindProxySlots()
		if err != nil {
			log.Printf("[scheduler] realtime probe heal slots: %v", err)
		} else if changed > 0 {
			log.Printf("[scheduler] realtime probe healed %d slot(s) after %d failure(s)", changed, failures)
			if err := s.reconcileManager(); err != nil {
				log.Printf("[scheduler] realtime probe reconcile after heal: %v", err)
			}
		}
	}
	if checked > 0 {
		log.Printf("[scheduler] realtime probe checked %d slot(s), failures=%d", checked, failures)
	}
}

func (s *Scheduler) acceptSlotPublicIP(slot *models.ProxySlot, publicIP string) (accepted, restartTunnel bool) {
	if strings.TrimSpace(publicIP) == "" {
		_ = s.db.MarkProxySlotProbeFailure(slot.ID, formatSlotProbeFailure(slot.ProbeFailures+1, "trace response missing public IP"))
		return false, false
	}
	if strings.TrimSpace(slot.PinnedPublicIP) == "" {
		if err := s.db.SetProxySlotPinnedIP(slot.ID, publicIP); err != nil {
			log.Printf("[scheduler] pin stable IP for slot %s failed: %v", slot.Username, err)
		} else {
			log.Printf("[scheduler] proxy slot %s pinned stable IP %s", slot.Username, publicIP)
		}
		return true, false
	}
	if slot.PinnedPublicIP == publicIP {
		return true, false
	}
	_ = s.db.MarkProxySlotIPDrift(slot.ID, formatSlotIPDrift(slot.IPDriftFailures+1, slot.PinnedPublicIP, publicIP))
	log.Printf("[scheduler] proxy slot %s detected IP drift on %s: expected %s, got %s",
		slot.Username, slot.AccountTag, slot.PinnedPublicIP, publicIP)
	return false, true
}

func (s *Scheduler) autoRefill(ctx context.Context) {
	if !s.hasAdminUser() {
		return
	}
	enabled, _, _ := s.db.GetSetting(db.SettingAutoGeneration)
	if enabled != "on" {
		return
	}
	if !s.tryStartWork() {
		return
	}
	defer s.finishWork()

	if wait := time.Until(s.refillBackoffUntil); wait > 0 {
		log.Printf("[scheduler] auto-refill backoff active for %.0fs", wait.Seconds())
		if changed, err := s.healProxySlots(ctx, false); err != nil {
			log.Printf("[scheduler] auto-refill backoff heal slots: %v", err)
		} else if changed > 0 {
			log.Printf("[scheduler] auto-refill backoff healed %d slot(s)", changed)
			if err := s.reconcileManager(); err != nil {
				log.Printf("[scheduler] auto-refill backoff reconcile after heal: %v", err)
			}
		}
		return
	}

	slotCount := s.proxySlotCount()
	if err := s.db.EnsureProxySlots(slotCount); err != nil {
		log.Printf("[scheduler] auto-refill ensure proxy slots: %v", err)
		return
	}
	if deleted, err := s.pruneStoredBadAccounts(); err != nil {
		log.Printf("[scheduler] auto-refill prune bad accounts: %v", err)
		return
	} else if deleted > 0 {
		log.Printf("[scheduler] auto-refill pruned %d bad/slow account(s)", deleted)
	}

	active, err := s.db.ListActiveAccounts()
	if err != nil {
		log.Printf("[scheduler] auto-refill list active accounts: %v", err)
		return
	}
	healthy := healthyAccountCount(active)
	if len(active) > 0 && healthy == 0 && s.manager.RunningCount() == 0 {
		log.Printf("[scheduler] auto-refill skipped: active WARP accounts exist but no healthy tunnel is running; waiting for WARP connectivity to recover")
		return
	}
	target := s.targetWarpPoolSize(slotCount)
	need := target - healthy
	if need <= 0 {
		if _, err := s.healProxySlots(ctx, true); err != nil {
			log.Printf("[scheduler] auto-refill heal slots: %v", err)
		}
		return
	}
	if need > maxAutoRegisterBatch {
		need = maxAutoRegisterBatch
	}

	log.Printf("[scheduler] auto-refill: active=%d healthy=%d target_pool=%d, registering %d account(s)", len(active), healthy, target, need)
	runID, _ := s.db.StartRun("auto-generate")
	inserted, err := s.GenerateAccounts(ctx, need)
	if err != nil {
		s.noteRefillError(err)
		_ = s.db.FinishRun(runID, "failed", fmt.Sprintf("auto-refill: inserted %d before error: %v", inserted, err), nil, nil)
		log.Printf("[scheduler] auto-refill failed after %d inserts: %v", inserted, err)
		return
	}
	changed, err := s.healProxySlots(ctx, true)
	if err != nil {
		_ = s.db.FinishRun(runID, "failed", fmt.Sprintf("auto-refill: registered %d, heal slots: %v", inserted, err), nil, nil)
		log.Printf("[scheduler] auto-refill heal failed after %d inserts: %v", inserted, err)
		return
	}
	_ = s.db.FinishRun(runID, "success",
		fmt.Sprintf("registered %d account(s), target pool %d, changed %d slot(s)", inserted, target, changed), nil, nil)
	log.Printf("[scheduler] auto-refill done: registered %d account(s), changed %d slot(s)", inserted, changed)
}

func (s *Scheduler) hasAdminUser() bool {
	count, err := s.db.CountUsers()
	return err == nil && count > 0
}

func (s *Scheduler) noteRefillError(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	delay := 5 * time.Minute
	if strings.Contains(msg, "1015") || strings.Contains(msg, "429") {
		delay = 30 * time.Minute
	}
	s.refillBackoffUntil = time.Now().Add(delay)
	log.Printf("[scheduler] auto-refill backoff until %s after error: %v", s.refillBackoffUntil.Format(time.RFC3339), err)
}

func (s *Scheduler) Trigger(kind string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return fmt.Errorf("已有调度任务正在运行，请等当前任务完成后再试")
	}
	select {
	case s.manualCh <- kind:
		return nil
	default:
		return fmt.Errorf("run queue full")
	}
}

func (s *Scheduler) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *Scheduler) LastRunAt() time.Time {
	return s.lastRunAt
}

func (s *Scheduler) RunningTunnels() int {
	return s.manager.RunningCount()
}

func (s *Scheduler) RunningTags() []string {
	return s.manager.RunningTags()
}

func (s *Scheduler) ProxySlotCount() int {
	return s.proxySlotCount()
}

func (s *Scheduler) TargetWarpPoolSize() int {
	return s.targetWarpPoolSize(s.proxySlotCount())
}

func (s *Scheduler) execute(ctx context.Context, kind string) bool {
	if !s.tryStartWork() {
		return false
	}
	defer s.finishWork()

	switch kind {
	case "dedup":
		s.runDedup(ctx)
	case "test":
		s.runTest(ctx)
	case "reconcile", "generate", "restart":
		s.runReconcile(ctx)
	default:
		s.runDedup(ctx)
	}
	return true
}

func (s *Scheduler) tryStartWork() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

func (s *Scheduler) finishWork() {
	s.mu.Lock()
	s.running = false
	s.mu.Unlock()
}

func (s *Scheduler) runDedup(ctx context.Context) {
	runID, _ := s.db.StartRun("dedup")
	log.Printf("[scheduler] dedup run %d started", runID)

	if err := s.db.EnsureProxySlots(s.proxySlotCount()); err != nil {
		_ = s.db.FinishRun(runID, "failed", fmt.Sprintf("ensure slots: %v", err), nil, nil)
		return
	}
	if err := s.reconcileManager(); err != nil {
		log.Printf("[scheduler] reconcile before dedup: %v", err)
	}
	deletedStored, err := s.pruneStoredBadAccounts()
	if err != nil {
		_ = s.db.FinishRun(runID, "failed", fmt.Sprintf("prune stored accounts: %v", err), nil, nil)
		return
	}

	accounts, err := s.db.ListActiveAccounts()
	if err != nil {
		_ = s.db.FinishRun(runID, "failed", fmt.Sprintf("list active accounts: %v", err), nil, nil)
		return
	}
	if len(accounts) == 0 {
		changed, healErr := s.healProxySlots(ctx, true)
		if healErr != nil {
			_ = s.db.FinishRun(runID, "failed", fmt.Sprintf("no active accounts; heal slots: %v", healErr), nil, nil)
			return
		}
		_ = s.db.FinishRun(runID, "success", fmt.Sprintf("no active accounts; changed %d slot(s)", changed), nil, nil)
		return
	}

	results := s.testAccounts(ctx, accounts)
	deletedBad := s.pruneTestResults(accounts, results)
	kept, disabled, uniqueIPs := s.applyDedup(accounts, results)
	allowRegister := uniqueIPs > 0 || deletedStored+deletedBad+disabled > 0
	changed, healErr := s.healProxySlots(ctx, allowRegister)
	if healErr != nil {
		_ = s.db.FinishRun(runID, "failed", fmt.Sprintf("heal slots: %v", healErr), &kept, &disabled)
		return
	}
	if err := s.reconcileManager(); err != nil {
		_ = s.db.FinishRun(runID, "failed", fmt.Sprintf("reconcile: %v", err), &kept, &disabled)
		return
	}

	detail := fmt.Sprintf("tested %d accounts, %d unique IPs, kept %d, deleted %d bad/slow and %d duplicate, changed %d slot(s)",
		len(accounts), uniqueIPs, kept, deletedStored+deletedBad, disabled, changed)
	if uniqueIPs == 0 {
		detail += ", skipped auto-register because every active WARP test failed"
	}
	_ = s.db.FinishRun(runID, "success", detail, &kept, &disabled)
	log.Printf("[scheduler] dedup run %d done: %s", runID, detail)
}

func (s *Scheduler) runTest(ctx context.Context) {
	runID, _ := s.db.StartRun("test")
	if err := s.reconcileManager(); err != nil {
		log.Printf("[scheduler] reconcile before test: %v", err)
	}
	accounts, err := s.db.ListActiveAccounts()
	if err != nil {
		_ = s.db.FinishRun(runID, "failed", err.Error(), nil, nil)
		return
	}
	results := s.testAccounts(ctx, accounts)
	failures := 0
	for _, r := range results {
		if r.Err != nil {
			failures++
		}
	}
	deletedBad := s.pruneTestResults(accounts, results)
	kept, deletedDup, uniqueIPs := s.applyDedup(accounts, results)
	allowRegister := uniqueIPs > 0 || failures < len(accounts) || deletedBad+deletedDup > 0
	changed, healErr := s.healProxySlots(ctx, allowRegister)
	if healErr != nil {
		_ = s.db.FinishRun(runID, "failed", fmt.Sprintf("tested %d accounts, heal slots: %v", len(accounts), healErr), nil, nil)
		return
	}
	_ = s.reconcileManager()
	detail := fmt.Sprintf("tested %d accounts, failures %d, kept %d, deleted %d bad/slow and %d duplicate, changed %d slot(s)",
		len(accounts), failures, kept, deletedBad, deletedDup, changed)
	_ = s.db.FinishRun(runID, "success", detail, &kept, &deletedDup)
}

func (s *Scheduler) TestOne(id int64) {
	go func() {
		ctx := context.Background()
		if err := s.reconcileManager(); err != nil {
			log.Printf("[scheduler] test-one reconcile: %v", err)
		}
		a, err := s.db.GetAccount(id)
		if err != nil || a == nil {
			log.Printf("[scheduler] test-one: account %d not found: %v", id, err)
			return
		}
		password, _, _ := s.db.GetSetting(db.SettingProxyPassword)
		tester := test.NewTester(s.proxyPort(), password)
		r := tester.TestAccount(ctx, a)
		if r.Err != nil {
			_ = s.db.UpdateAccountTestError(id, r.Err.Error())
			log.Printf("[scheduler] test-one %s failed: %v", a.Tag, r.Err)
			updated, _ := s.db.GetAccount(id)
			if updated != nil && updated.Status == "error" {
				_ = s.db.DeleteAccount(id)
				log.Printf("[scheduler] deleted failed WARP %s after repeated retests: %v", a.Tag, r.Err)
			}
		} else {
			_ = s.db.UpdateAccountTestResult(id, r.PublicIP, r.Colo, r.Country, r.LatencyMs, r.SpeedBps, r.PacketLoss, r.Score)
			log.Printf("[scheduler] test-one %s: ip=%s score=%.0f", a.Tag, r.PublicIP, r.Score)
			if bad, reason := badQuality(r.LatencyMs, r.SpeedBps, r.PacketLoss); bad {
				_ = s.db.DeleteAccount(id)
				log.Printf("[scheduler] deleted low-quality WARP %s after retest: %s", a.Tag, reason)
			}
		}
		if _, err := s.healProxySlots(ctx, true); err != nil {
			log.Printf("[scheduler] test-one heal slots: %v", err)
		}
		_ = s.reconcileManager()
	}()
}

func (s *Scheduler) runReconcile(ctx context.Context) {
	runID, _ := s.db.StartRun("reconcile")
	if err := s.reconcileManager(); err != nil {
		log.Printf("[scheduler] reconcile before slot heal: %v", err)
	}
	changed, err := s.healProxySlots(ctx, false)
	if err != nil {
		_ = s.db.FinishRun(runID, "failed", err.Error(), nil, nil)
		return
	}
	if err := s.reconcileManager(); err != nil {
		_ = s.db.FinishRun(runID, "failed", err.Error(), nil, nil)
		return
	}
	_ = s.db.FinishRun(runID, "success", fmt.Sprintf("tunnels reconciled, changed %d slot(s)", changed), nil, nil)
}

func (s *Scheduler) Reconcile() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	if err := s.reconcileManager(); err != nil {
		log.Printf("[scheduler] initial reconcile before slot heal: %v", err)
	}
	if _, err := s.healProxySlots(context.Background(), false); err != nil {
		return err
	}
	return s.reconcileManager()
}

func (s *Scheduler) GenerateAccounts(ctx context.Context, n int) (int, error) {
	if n <= 0 {
		return 0, nil
	}
	inserted := 0
	insertedAccounts := make([]*models.Account, 0, n)
	defer func() {
		if inserted > 0 {
			if err := s.reconcileManager(); err != nil {
				log.Printf("[scheduler] reconcile after registration: %v", err)
			}
			if ctx.Err() == nil {
				results := s.testAccounts(ctx, insertedAccounts)
				deleted := s.pruneTestResults(insertedAccounts, results)
				if deleted > 0 {
					log.Printf("[scheduler] deleted %d new WARP account(s) after initial quality test", deleted)
					if err := s.reconcileManager(); err != nil {
						log.Printf("[scheduler] reconcile after initial quality prune: %v", err)
					}
				}
				changed, _, err := s.rebindProxySlots()
				if err != nil {
					log.Printf("[scheduler] rebind after initial quality test: %v", err)
				} else if changed > 0 {
					log.Printf("[scheduler] rebound %d proxy slot(s) after initial quality test", changed)
					if err := s.reconcileManager(); err != nil {
						log.Printf("[scheduler] reconcile after initial quality rebind: %v", err)
					}
				}
			}
		}
	}()
	for i := 0; i < n; i++ {
		var acct *warp.Account
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return inserted, ctx.Err()
				case <-time.After(time.Duration(1<<attempt) * time.Second):
				}
			}
			acct, lastErr = s.warp.Register(ctx)
			if lastErr == nil {
				break
			}
		}
		if lastErr != nil {
			log.Printf("[scheduler] register failed: %v", lastErr)
			return inserted, lastErr
		}

		tag, err := s.db.NextTag()
		if err != nil {
			return inserted, fmt.Errorf("next tag: %w", err)
		}
		masqueCfg, err := s.warp.EnrollMasque(ctx, acct.DeviceID, acct.AccessToken, tag)
		if err != nil {
			log.Printf("[scheduler] enroll MASQUE for %s failed: %v", tag, err)
			return inserted, err
		}
		acct.MasquePrivateKey = masqueCfg.PrivateKey
		acct.MasqueEndpointPubKey = masqueCfg.EndpointPubKey
		acct.MasqueEndpointV4 = masqueCfg.EndpointV4
		acct.MasqueEndpointV6 = masqueCfg.EndpointV6
		if masqueCfg.AddressV4 != "" {
			acct.AddressV4 = masqueCfg.AddressV4
		}
		if masqueCfg.AddressV6 != "" {
			acct.AddressV6 = masqueCfg.AddressV6
		}
		endpointHost, endpointPort := splitEndpoint(acct.EndpointHost)
		a := &models.Account{
			Tag:                  tag,
			Directory:            "",
			Status:               "active",
			PrivateKey:           acct.PrivateKey,
			ClientID:             acct.ClientID,
			AccessToken:          acct.AccessToken,
			DeviceID:             acct.DeviceID,
			LicenseKey:           acct.License,
			PeerPublicKey:        acct.PeerPublicKey,
			LocalAddressV4:       acct.AddressV4,
			LocalAddressV6:       acct.AddressV6,
			EndpointHost:         endpointHost,
			EndpointPort:         endpointPort,
			MTU:                  1280,
			ListenPort:           0,
			MasquePrivateKey:     acct.MasquePrivateKey,
			MasqueEndpointPubKey: acct.MasqueEndpointPubKey,
			MasqueEndpointV4:     acct.MasqueEndpointV4,
			MasqueEndpointV6:     acct.MasqueEndpointV6,
		}
		if err := s.db.InsertAccount(a); err != nil {
			log.Printf("[scheduler] insert account %s: %v", tag, err)
			continue
		}
		insertedAccounts = append(insertedAccounts, a)
		inserted++
	}
	return inserted, nil
}

func (s *Scheduler) healProxySlots(ctx context.Context, allowRegister bool) (int, error) {
	slotCount := s.proxySlotCount()
	if err := s.db.EnsureProxySlots(slotCount); err != nil {
		return 0, err
	}

	changed, missing, err := s.rebindProxySlots()
	if err != nil || missing == 0 || !allowRegister {
		if changed > 0 {
			_ = s.reconcileManager()
		}
		return changed, err
	}

	active, err := s.db.ListActiveAccounts()
	if err != nil {
		return changed, err
	}
	need := s.targetWarpPoolSize(slotCount) - healthyAccountCount(active)
	if need < missing {
		need = missing
	}
	if need < minWarpPoolReserve {
		need = minWarpPoolReserve
	}
	if need > maxAutoRegisterBatch {
		need = maxAutoRegisterBatch
	}

	inserted, err := s.GenerateAccounts(ctx, need)
	if err != nil {
		s.noteRefillError(err)
		return changed, err
	}
	if inserted == 0 {
		return changed, nil
	}
	if err := s.reconcileManager(); err != nil {
		log.Printf("[scheduler] reconcile before testing new accounts: %v", err)
	}
	if _, _, err := s.testActiveAndDedupe(ctx); err != nil {
		return changed, err
	}
	moreChanged, _, err := s.rebindProxySlots()
	if err != nil {
		return changed + moreChanged, err
	}
	if moreChanged > 0 {
		_ = s.reconcileManager()
	}
	return changed + moreChanged, nil
}

func (s *Scheduler) rebindProxySlots() (changed, missing int, err error) {
	slots, err := s.db.ListProxySlots()
	if err != nil {
		return 0, 0, err
	}
	accounts, err := s.db.ListAccounts()
	if err != nil {
		return 0, 0, err
	}
	running := s.runningTagSet()

	byID := make(map[int64]*models.Account, len(accounts))
	for _, a := range accounts {
		byID[a.ID] = a
	}
	blockedAccount := map[int64]bool{}
	for _, slot := range slots {
		if slot.AccountID != nil && (slot.ProbeFailures >= slotProbeRebindAfter || slot.IPDriftFailures >= slotIPDriftRebindAfter) {
			blockedAccount[*slot.AccountID] = true
		}
	}
	candidates := rankedRunningAccounts(accounts, running)
	candidates = filterBlockedAccounts(candidates, blockedAccount)
	usedAccount := map[int64]bool{}
	usedIP := map[string]bool{}

	for _, slot := range slots {
		if slot.Status != "active" {
			continue
		}
		var current *models.Account
		if slot.AccountID != nil {
			current = byID[*slot.AccountID]
		}
		if keepCurrentSlotBinding(current, slot, usedAccount, usedIP) {
			usedAccount[current.ID] = true
			if ip := slotEffectiveIP(slot, current); ip != "" {
				usedIP[ip] = true
			}
			if slot.LastError != "" && slot.ProbeFailures == 0 && slot.IPDriftFailures == 0 {
				_ = s.db.MarkProxySlotError(slot.ID, "")
				changed++
			}
			continue
		}

		candidate := nextSlotCandidate(slot, candidates, usedAccount, usedIP)
		if candidate == nil {
			missing++
			if current != nil {
				usedAccount[current.ID] = true
				if ip := slotEffectiveIP(slot, current); ip != "" {
					usedIP[ip] = true
				}
				if slot.LastError == "" {
					_ = s.db.MarkProxySlotError(slot.ID, "没有可替换的健康 WARP，继续保留当前绑定")
				}
				continue
			}
			_ = s.db.MarkProxySlotError(slot.ID, "no healthy unique WARP account available")
			continue
		}
		if err := s.db.AssignProxySlotAccount(slot.ID, candidate.ID); err != nil {
			return changed, missing, err
		}
		usedAccount[candidate.ID] = true
		if candidate.LastPublicIP != "" {
			usedIP[candidate.LastPublicIP] = true
		}
		changed++
		log.Printf("[scheduler] proxy slot %s bound to %s (%s)", slot.Username, candidate.Tag, candidate.LastPublicIP)
	}
	return changed, missing, nil
}

func keepCurrentSlotBinding(current *models.Account, slot *models.ProxySlot, usedAccount map[int64]bool, usedIP map[string]bool) bool {
	if current == nil || current.Status != "active" || usedAccount[current.ID] {
		return false
	}
	if slot.ProbeFailures >= slotProbeRebindAfter || slot.IPDriftFailures >= slotIPDriftRebindAfter {
		return false
	}
	if ip := slotEffectiveIP(slot, current); ip != "" && usedIP[ip] {
		return false
	}
	return true
}

func filterBlockedAccounts(accounts []*models.Account, blocked map[int64]bool) []*models.Account {
	if len(blocked) == 0 {
		return accounts
	}
	out := accounts[:0]
	for _, a := range accounts {
		if !blocked[a.ID] {
			out = append(out, a)
		}
	}
	return out
}

func isSlotProbeFailure(msg string) bool {
	return strings.HasPrefix(msg, slotProbeFailure)
}

func formatSlotProbeFailure(failures int, reason string) string {
	if failures < 1 {
		failures = 1
	}
	return fmt.Sprintf("%s%d/%d: %s", slotProbeFailure, failures, slotProbeRebindAfter, reason)
}

func formatSlotIPDrift(failures int, expected, actual string) string {
	if failures < 1 {
		failures = 1
	}
	return fmt.Sprintf("%s%d/%d: 稳定 IP %s，当前检测到 %s；暂不接受新 IP",
		slotIPDriftFailure, failures, slotIPDriftRebindAfter, expected, actual)
}

func nextSlotCandidate(slot *models.ProxySlot, candidates []*models.Account, usedAccount map[int64]bool, usedIP map[string]bool) *models.Account {
	if slot != nil && slot.PinnedPublicIP != "" {
		for _, a := range candidates {
			if !slotCandidateAvailable(a, usedAccount, usedIP) {
				continue
			}
			if a.LastPublicIP == slot.PinnedPublicIP {
				return a
			}
		}
	}
	for _, a := range candidates {
		if !slotCandidateAvailable(a, usedAccount, usedIP) {
			continue
		}
		return a
	}
	return nil
}

func slotCandidateAvailable(a *models.Account, usedAccount map[int64]bool, usedIP map[string]bool) bool {
	if a == nil || usedAccount[a.ID] {
		return false
	}
	if a.LastPublicIP != "" && usedIP[a.LastPublicIP] {
		return false
	}
	return true
}

func slotEffectiveIP(slot *models.ProxySlot, current *models.Account) string {
	if slot != nil && slot.PinnedPublicIP != "" {
		return slot.PinnedPublicIP
	}
	if current != nil {
		return current.LastPublicIP
	}
	return ""
}

func rankedAccounts(accounts []*models.Account) []*models.Account {
	return rankedAccountsWithRunning(accounts, nil)
}

func rankedRunningAccounts(accounts []*models.Account, running map[string]bool) []*models.Account {
	return rankedAccountsWithRunning(accounts, running)
}

func rankedAccountsWithRunning(accounts []*models.Account, running map[string]bool) []*models.Account {
	out := make([]*models.Account, 0, len(accounts))
	for _, a := range accounts {
		if accountUsable(a) && (running == nil || running[a.Tag]) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		si := accountQualityScore(out[i])
		sj := accountQualityScore(out[j])
		if si == sj {
			return out[i].Tag < out[j].Tag
		}
		return si < sj
	})
	return out
}

func (s *Scheduler) runningTagSet() map[string]bool {
	tags := s.manager.RunningTags()
	out := make(map[string]bool, len(tags))
	for _, tag := range tags {
		out[tag] = true
	}
	return out
}

func healthyAccountCount(accounts []*models.Account) int {
	count := 0
	for _, a := range accounts {
		if accountUsable(a) {
			count++
		}
	}
	return count
}

func accountUsable(a *models.Account) bool {
	if a == nil || a.Status != "active" {
		return false
	}
	if a.LastTestedAt == nil {
		return false
	}
	if a.LastTestedAt != nil && a.LastPublicIP == "" {
		return false
	}
	if a.LastTestedAt != nil && a.LastPacketLoss >= 0.80 {
		return false
	}
	if a.LastTestedAt != nil {
		if bad, _ := badStoredQuality(a); bad {
			return false
		}
	}
	return true
}

func allTestedActiveAccountsFailed(accounts []*models.Account) bool {
	if len(accounts) == 0 {
		return false
	}
	tested := 0
	for _, a := range accounts {
		if a.LastTestedAt == nil {
			continue
		}
		tested++
		if a.LastPublicIP != "" {
			return false
		}
	}
	return tested == len(accounts)
}

func accountQualityScore(a *models.Account) float64 {
	score := a.LastScore
	if score <= 0 {
		score = 100000
	}
	if a.LastLatencyMs > 0 {
		score += float64(a.LastLatencyMs) * 0.05
	} else {
		score += 500
	}
	if a.LastSpeedBps > 0 {
		score += 100000000.0 / float64(a.LastSpeedBps)
	} else {
		score += 500
	}
	score += a.LastPacketLoss * 2000
	score += accountColoPenalty(a.LastColo)
	if a.IsIPKeeper {
		score -= 250
	}
	if a.LastTestedAt == nil {
		score += 1500
	} else if time.Since(*a.LastTestedAt) > 24*time.Hour {
		score += 500
	}
	return score
}

func (s *Scheduler) pruneStoredBadAccounts() (int, error) {
	accounts, err := s.db.ListAccounts()
	if err != nil {
		return 0, err
	}
	bound := s.boundAccountIDs()
	deleted := 0
	for _, a := range accounts {
		if bound[a.ID] {
			continue
		}
		reason := ""
		if a.Status == "error" {
			reason = firstNonEmpty(a.DisabledReason, "too many consecutive test failures")
		} else if a.Status != "active" {
			continue
		} else if a.LastTestedAt == nil {
			age := time.Since(a.CreatedAt)
			if a.CreatedAt.IsZero() || age > untestedAccountGrace {
				reason = fmt.Sprintf("untested for %.0fs", age.Seconds())
			}
		} else if a.LastTestedAt != nil {
			if bad, why := badStoredQuality(a); bad {
				reason = why
			}
		}
		if reason == "" {
			continue
		}
		if err := s.db.DeleteAccount(a.ID); err != nil {
			return deleted, fmt.Errorf("delete %s: %w", a.Tag, err)
		}
		deleted++
		log.Printf("[scheduler] deleted WARP %s during prune: %s", a.Tag, reason)
	}
	return deleted, nil
}

func (s *Scheduler) pruneTestResults(accounts []*models.Account, results map[int64]test.Result) int {
	bound := s.boundAccountIDs()
	deleted := 0
	for _, a := range accounts {
		if bound[a.ID] {
			continue
		}
		r, ok := results[a.ID]
		if !ok {
			continue
		}
		reason := ""
		if r.Err != nil {
			updated, _ := s.db.GetAccount(a.ID)
			if updated == nil || updated.Status != "error" {
				continue
			}
			reason = firstNonEmpty(updated.DisabledReason, r.Err.Error())
		} else if r.PublicIP == "" {
			reason = "trace response missing public IP"
		} else if bad, why := badQuality(r.LatencyMs, r.SpeedBps, r.PacketLoss); bad {
			reason = why
		}
		if reason == "" {
			continue
		}
		if err := s.db.DeleteAccount(a.ID); err != nil {
			log.Printf("[scheduler] delete bad WARP %s failed: %v", a.Tag, err)
			continue
		}
		deleted++
		log.Printf("[scheduler] deleted bad WARP %s: %s", a.Tag, reason)
	}
	return deleted
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func badStoredQuality(a *models.Account) (bool, string) {
	if a.LastPublicIP == "" {
		return false, ""
	}
	return badQuality(a.LastLatencyMs, a.LastSpeedBps, a.LastPacketLoss)
}

func badQuality(latencyMs, speedBps int, packetLoss float64) (bool, string) {
	if packetLoss >= maxHealthyPacketLoss {
		return true, fmt.Sprintf("packet loss %.0f%% >= %.0f%%", packetLoss*100, maxHealthyPacketLoss*100)
	}
	if latencyMs <= 0 {
		return true, "latency not measured"
	}
	if latencyMs > maxHealthyLatencyMs {
		return true, fmt.Sprintf("latency %dms > %dms", latencyMs, maxHealthyLatencyMs)
	}
	if speedBps <= 0 {
		return true, "speed not measured"
	}
	if speedBps < minHealthySpeedBps {
		return true, fmt.Sprintf("speed %.2fKB/s < %.2fKB/s", float64(speedBps)/1024.0, float64(minHealthySpeedBps)/1024.0)
	}
	return false, ""
}

func accountColoPenalty(colo string) float64 {
	switch strings.ToUpper(strings.TrimSpace(colo)) {
	case "HKG", "TPE", "NRT", "KIX", "ICN":
		return 0
	case "SIN":
		return 120
	case "SJC", "SEA":
		return 450
	case "LAX":
		return 900
	case "DFW", "ORD", "IAD", "EWR", "ATL", "MIA", "DEN", "PHX", "PDX":
		return 1200
	case "":
		return 300
	default:
		return 600
	}
}

func (s *Scheduler) testActiveAndDedupe(ctx context.Context) (tested, disabled int, err error) {
	accounts, err := s.db.ListActiveAccounts()
	if err != nil {
		return 0, 0, err
	}
	if len(accounts) == 0 {
		return 0, 0, nil
	}
	results := s.testAccounts(ctx, accounts)
	_, disabled, _ = s.applyDedup(accounts, results)
	return len(accounts), disabled, nil
}

func (s *Scheduler) testAccounts(ctx context.Context, accounts []*models.Account) map[int64]test.Result {
	password, _, _ := s.db.GetSetting(db.SettingProxyPassword)
	tester := test.NewTester(s.proxyPort(), password)
	results := tester.RunBatch(ctx, accounts, 2)
	for id, r := range results {
		if r.Err != nil {
			_ = s.db.UpdateAccountTestError(id, r.Err.Error())
			continue
		}
		_ = s.db.UpdateAccountTestResult(id, r.PublicIP, r.Colo, r.Country, r.LatencyMs, r.SpeedBps, r.PacketLoss, r.Score)
	}
	return results
}

func (s *Scheduler) applyDedup(accounts []*models.Account, results map[int64]test.Result) (kept, disabled, uniqueIPs int) {
	bound := s.boundAccountIDs()
	groups := map[string][]*models.Account{}
	for _, a := range accounts {
		r, ok := results[a.ID]
		if !ok || r.Err != nil || r.PublicIP == "" {
			continue
		}
		updated, _ := s.db.GetAccount(a.ID)
		if updated != nil {
			a = updated
		} else {
			continue
		}
		groups[r.PublicIP] = append(groups[r.PublicIP], a)
	}

	for ip, group := range groups {
		if len(group) == 0 {
			continue
		}
		keeper := group[0]
		for _, a := range group[1:] {
			if betterIPKeeper(a, keeper, bound) {
				keeper = a
			}
		}
		_ = s.db.SetIPKeeper(ip, keeper.ID)
		_ = s.db.ClearIPKeeperExcept(ip, keeper.ID)
		for _, a := range group {
			if a.ID == keeper.ID {
				kept++
			} else {
				if bound[a.ID] {
					kept++
					log.Printf("[scheduler] kept duplicate bound WARP %s for IP %s; waiting for replacement before rebinding", a.Tag, ip)
					continue
				}
				if err := s.db.DeleteAccount(a.ID); err != nil {
					log.Printf("[scheduler] delete duplicate WARP %s for IP %s failed: %v", a.Tag, ip, err)
					continue
				}
				log.Printf("[scheduler] deleted duplicate WARP %s for IP %s; kept %s", a.Tag, ip, keeper.Tag)
				disabled++
			}
		}
	}
	return kept, disabled, len(groups)
}

func betterIPKeeper(candidate, current *models.Account, bound map[int64]bool) bool {
	candidateBound := bound[candidate.ID]
	currentBound := bound[current.ID]
	if candidateBound != currentBound {
		return candidateBound
	}
	return accountQualityScore(candidate) < accountQualityScore(current)
}

func (s *Scheduler) boundAccountIDs() map[int64]bool {
	slots, err := s.db.ListProxySlots()
	if err != nil {
		return map[int64]bool{}
	}
	out := make(map[int64]bool, len(slots))
	for _, slot := range slots {
		if slot.Status == "active" && slot.AccountID != nil {
			out[*slot.AccountID] = true
		}
	}
	return out
}

func (s *Scheduler) reconcileManager() error {
	s.refreshMissingClientIDs(context.Background())
	s.refreshMissingMasque(context.Background())
	password, _, _ := s.db.GetSetting(db.SettingProxyPassword)
	s.manager.SetPassword(password)
	bindAddr, _, _ := s.db.GetSetting(db.SettingProxyListenAddr)
	if bindAddr == "" {
		bindAddr = "0.0.0.0"
	}
	s.manager.SetBindAddr(bindAddr)
	s.manager.SetProxyPort(s.proxyPort())
	transport, _, _ := s.db.GetSetting(db.SettingWarpTransport)
	if transport == "" {
		transport = "auto"
	}
	s.manager.SetTransport(transport)
	ipFamily, _, _ := s.db.GetSetting(db.SettingTunnelIPFamily)
	if ipFamily == "" {
		ipFamily = "ipv4"
	}
	s.manager.SetIPFamily(ipFamily)
	dnsMode, _, _ := s.db.GetSetting(db.SettingProxyDNSMode)
	if dnsMode == "" {
		dnsMode = "system"
	}
	s.manager.SetDNSMode(dnsMode)
	tlsSetting, _, _ := s.db.GetSetting(db.SettingProxyTLS)
	tlsEnabled := tlsSetting != "off"
	// TLS 证书的 SAN 用「代理对外地址」，方便按主机名校验的客户端；留空也没关系，
	// 客户端用 skip-cert-verify 连接。IP 形式会写成 IP SAN。
	tlsServerName, _, _ := s.db.GetSetting(db.SettingProxyPublicHost)
	s.manager.SetProxyTLS(tlsEnabled, strings.TrimSpace(tlsServerName))
	return s.manager.Reconcile()
}

func (s *Scheduler) refreshMissingClientIDs(ctx context.Context) {
	accounts, err := s.db.ListActiveAccounts()
	if err != nil {
		log.Printf("[scheduler] refresh client_id list accounts: %v", err)
		return
	}
	for _, a := range accounts {
		if a.ClientID != "" || a.DeviceID == "" || a.AccessToken == "" {
			continue
		}
		cfg, err := s.warp.GetDeviceConfig(ctx, a.DeviceID, a.AccessToken)
		if err != nil {
			log.Printf("[scheduler] refresh client_id for %s failed: %v", a.Tag, err)
			continue
		}
		if cfg.ClientID == "" {
			log.Printf("[scheduler] refresh client_id for %s returned empty client_id", a.Tag)
			continue
		}
		if err := s.db.UpdateAccountClientID(a.ID, cfg.ClientID); err != nil {
			log.Printf("[scheduler] save client_id for %s failed: %v", a.Tag, err)
			continue
		}
		log.Printf("[scheduler] refreshed WARP client_id for %s", a.Tag)
	}
}

func (s *Scheduler) refreshMissingMasque(ctx context.Context) {
	transport, _, _ := s.db.GetSetting(db.SettingWarpTransport)
	transport = strings.ToLower(strings.TrimSpace(transport))
	if transport == "wireguard" {
		return
	}
	accounts, err := s.db.ListActiveAccounts()
	if err != nil {
		log.Printf("[scheduler] refresh MASQUE list accounts: %v", err)
		return
	}
	for _, a := range accounts {
		if !accountNeedsMasque(a) {
			continue
		}
		if a.DeviceID == "" || a.AccessToken == "" {
			log.Printf("[scheduler] refresh MASQUE skipped for %s: missing device token", a.Tag)
			continue
		}
		cfg, err := s.warp.EnrollMasque(ctx, a.DeviceID, a.AccessToken, a.Tag)
		if err != nil {
			log.Printf("[scheduler] refresh MASQUE for %s failed: %v", a.Tag, err)
			continue
		}
		if err := s.db.UpdateAccountMasque(a.ID, cfg.PrivateKey, cfg.EndpointPubKey, cfg.EndpointV4, cfg.EndpointV6, cfg.AddressV4, cfg.AddressV6); err != nil {
			log.Printf("[scheduler] save MASQUE for %s failed: %v", a.Tag, err)
			continue
		}
		log.Printf("[scheduler] refreshed MASQUE config for %s", a.Tag)
	}
}

func accountNeedsMasque(a *models.Account) bool {
	return a.MasquePrivateKey == "" || a.MasqueEndpointPubKey == "" || a.MasqueEndpointV4 == ""
}

func (s *Scheduler) proxySlotCount() int {
	v, ok, _ := s.db.GetSetting(db.SettingProxySlotCount)
	if !ok || v == "" {
		v, _, _ = s.db.GetSetting(db.SettingTargetAccountCount)
	}
	var n int
	fmt.Sscanf(v, "%d", &n)
	if n <= 0 {
		n = db.DefaultProxyIPCount
	}
	return n
}

func (s *Scheduler) targetWarpPoolSize(slotCount int) int {
	if slotCount <= 0 {
		return 0
	}
	reserve := (slotCount + 1) / 2
	if reserve < minWarpPoolReserve {
		reserve = minWarpPoolReserve
	}
	return slotCount + reserve
}

func (s *Scheduler) proxyPort() int {
	proxyPort := 7843
	if v, _, _ := s.db.GetSetting(db.SettingProxyPort); v != "" {
		fmt.Sscanf(v, "%d", &proxyPort)
	}
	return proxyPort
}

func (s *Scheduler) dedupInterval() int {
	v, _, _ := s.db.GetSetting(db.SettingDedupIntervalSeconds)
	var n int
	fmt.Sscanf(v, "%d", &n)
	if n <= 0 {
		n = 600
	}
	return n
}

func (s *Scheduler) nextRunTime(interval int) time.Time {
	if s.lastRunAt.IsZero() {
		return time.Now().Add(30 * time.Second)
	}
	return s.lastRunAt.Add(time.Duration(interval) * time.Second)
}

func (s *Scheduler) trafficLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	type sample struct {
		tx int64
		rx int64
		at time.Time
	}
	lastTag := map[string]sample{}
	lastIP := map[string]sample{}

	// 整体吞吐采样：每 ~15s 落一条 traffic_samples，作为仪表盘时间序列的
	// 服务端数据源；每 ~1h 清理早于 24h 的采样，控制表大小。
	const (
		sampleEvery = 15 * time.Second
		samplePrune = 24 * time.Hour
		pruneEvery  = time.Hour
	)
	var lastTotalTx, lastTotalRx int64
	haveTotal := false
	lastSampleAt := time.Now()
	lastPruneAt := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		now := time.Now()
		snaps := s.manager.Snapshot()
		accounts, _ := s.db.ListAccounts()
		tagToIP := map[string]string{}
		for _, a := range accounts {
			if a.LastPublicIP != "" {
				tagToIP[a.Tag] = a.LastPublicIP
			}
		}

		ipAgg := map[string]sample{}
		for _, sn := range snaps {
			prev, ok := lastTag[sn.Tag]
			if ok {
				up := deltaBytes(sn.Tx, prev.tx)
				down := deltaBytes(sn.Rx, prev.rx)
				if up > 0 || down > 0 {
					_ = s.db.AddTrafficByTag(sn.Tag, up, down)
				}
			}
			lastTag[sn.Tag] = sample{tx: sn.Tx, rx: sn.Rx, at: now}

			if ip := tagToIP[sn.Tag]; ip != "" {
				a := ipAgg[ip]
				a.tx += sn.Tx
				a.rx += sn.Rx
				ipAgg[ip] = a
			}
		}

		for ip, cur := range ipAgg {
			prev, ok := lastIP[ip]
			if ok {
				upDelta := deltaBytes(cur.tx, prev.tx)
				downDelta := deltaBytes(cur.rx, prev.rx)
				elapsed := now.Sub(prev.at).Seconds()
				var upBps, downBps int64
				if elapsed > 0 {
					upBps = int64(float64(upDelta) / elapsed)
					downBps = int64(float64(downDelta) / elapsed)
				}
				_ = s.db.SetIPPoolCurrent(ip, upBps, downBps)
				if upDelta > 0 || downDelta > 0 {
					_ = s.db.AddIPPoolTraffic(ip, upDelta, downDelta)
				}
			} else {
				_ = s.db.SetIPPoolCurrent(ip, 0, 0)
			}
			cur.at = now
			lastIP[ip] = cur
		}

		for ip := range lastIP {
			if _, active := ipAgg[ip]; !active {
				_ = s.db.SetIPPoolCurrent(ip, 0, 0)
				delete(lastIP, ip)
			}
		}

		// 累计当前所有隧道的 tx/rx 总量，按采样间隔差分成 bps 落库。
		var totalTx, totalRx int64
		for _, sn := range snaps {
			totalTx += sn.Tx
			totalRx += sn.Rx
		}
		if now.Sub(lastSampleAt) >= sampleEvery {
			if haveTotal {
				elapsed := now.Sub(lastSampleAt).Seconds()
				var upBps, downBps int64
				if elapsed > 0 {
					upBps = int64(float64(deltaBytes(totalTx, lastTotalTx)) / elapsed)
					downBps = int64(float64(deltaBytes(totalRx, lastTotalRx)) / elapsed)
				}
				_ = s.db.AddTrafficSample(upBps, downBps)
			}
			lastTotalTx, lastTotalRx = totalTx, totalRx
			haveTotal = true
			lastSampleAt = now
		}
		if now.Sub(lastPruneAt) >= pruneEvery {
			_ = s.db.PruneTrafficSamples(samplePrune)
			lastPruneAt = now
		}
	}
}

func deltaBytes(cur, prev int64) int64 {
	d := cur - prev
	if d < 0 {
		return cur
	}
	return d
}

func splitEndpoint(endpoint string) (host string, port int) {
	port = 2408
	if endpoint == "" {
		return "engage.cloudflareclient.com", port
	}
	idx := -1
	for i := len(endpoint) - 1; i >= 0; i-- {
		if endpoint[i] == ':' {
			idx = i
			break
		}
	}
	if idx < 0 {
		return endpoint, port
	}
	host = endpoint[:idx]
	fmt.Sscanf(endpoint[idx+1:], "%d", &port)
	if port == 0 {
		port = 2408
	}
	return host, port
}
