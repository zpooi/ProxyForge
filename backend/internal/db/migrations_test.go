package db

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zpooi/ProxyForge/backend/internal/models"
)

func TestSeedDefaultsImportsWarpAccountMetadata(t *testing.T) {
	root := t.TempDir()
	accountDir := filepath.Join(root, "warp-accounts", "warp-001")
	if err := os.MkdirAll(accountDir, 0o755); err != nil {
		t.Fatal(err)
	}
	accountTOML := `access_token = 'token-1'
device_id = 'device-1'
license_key = 'license-1'
private_key = 'priv-1'
`
	profileConf := `[Interface]
PrivateKey = priv-1
Address = 172.16.0.2/32
Address = 2606:4700:110:8638:620b:fb29:e353:31a2/128
DNS = 1.1.1.1
MTU = 1280
[Peer]
PublicKey = peer-1
AllowedIPs = 0.0.0.0/0
AllowedIPs = ::/0
Endpoint = engage.cloudflareclient.com:2408
`
	if err := os.WriteFile(filepath.Join(accountDir, "wgcf-account.toml"), []byte(accountTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(accountDir, "wgcf-profile.conf"), []byte(profileConf), 0o600); err != nil {
		t.Fatal(err)
	}

	database, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := database.SeedDefaultsIfEmpty(root); err != nil {
		t.Fatal(err)
	}

	a, err := database.GetAccountByTag("warp-001")
	if err != nil {
		t.Fatal(err)
	}
	if a == nil {
		t.Fatal("expected imported account")
	}
	if a.Directory != "" {
		t.Fatalf("expected no runtime directory dependency, got %q", a.Directory)
	}
	if a.AccessToken != "token-1" || a.DeviceID != "device-1" || a.LicenseKey != "license-1" {
		t.Fatalf("metadata not imported: token=%q device=%q license=%q", a.AccessToken, a.DeviceID, a.LicenseKey)
	}
	if a.PrivateKey != "priv-1" || a.PeerPublicKey != "peer-1" || a.EndpointHost != "engage.cloudflareclient.com" || a.EndpointPort != 2408 {
		t.Fatalf("profile not imported: %#v", a)
	}
}

func TestSeedDefaultsUseProxyIPCountTwenty(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := database.SeedDefaultsIfEmpty(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	slotCount, ok, err := database.GetSetting(SettingProxySlotCount)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || slotCount != "20" {
		t.Fatalf("expected default proxy IP count 20, got %q ok=%v", slotCount, ok)
	}
	targetCount, ok, err := database.GetSetting(SettingTargetAccountCount)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || targetCount != "20" {
		t.Fatalf("expected default target count 20, got %q ok=%v", targetCount, ok)
	}
	slots, err := database.ListProxySlots()
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 20 {
		t.Fatalf("expected 20 default proxy IP slots, got %d", len(slots))
	}
	userCount, err := database.CountUsers()
	if err != nil {
		t.Fatal(err)
	}
	if userCount != 0 {
		t.Fatalf("new database should wait for web setup instead of seeding default admin, got %d user(s)", userCount)
	}
}

func TestAccountNeedsThreeConsecutiveFailuresBeforeError(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}

	account := &models.Account{
		Tag:            "warp-001",
		Directory:      "",
		Status:         "active",
		PrivateKey:     "private",
		PeerPublicKey:  "peer",
		LocalAddressV4: "172.16.0.2",
		EndpointHost:   "engage.cloudflareclient.com",
		EndpointPort:   2408,
		MTU:            1280,
	}
	if err := database.InsertAccount(account); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 2; i++ {
		if err := database.UpdateAccountTestError(account.ID, "timeout"); err != nil {
			t.Fatal(err)
		}
		a, err := database.GetAccount(account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if a.Status != "active" {
			t.Fatalf("failure %d should keep account active, got %q", i+1, a.Status)
		}
	}

	if err := database.UpdateAccountTestError(account.ID, "timeout"); err != nil {
		t.Fatal(err)
	}
	a, err := database.GetAccount(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "error" {
		t.Fatalf("third consecutive failure should mark error, got %q", a.Status)
	}
}

func TestEnsureProxySlotsCreatesActiveSlotsWithoutAutoBinding(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}

	var accountIDs []int64
	for i := 1; i <= 3; i++ {
		account := &models.Account{
			Tag:            databaseMustNextTag(t, database),
			Directory:      "",
			Status:         "active",
			PrivateKey:     "private",
			PeerPublicKey:  "peer",
			LocalAddressV4: "172.16.0.2",
			EndpointHost:   "engage.cloudflareclient.com",
			EndpointPort:   2408,
			MTU:            1280,
		}
		if err := database.InsertAccount(account); err != nil {
			t.Fatal(err)
		}
		accountIDs = append(accountIDs, account.ID)
	}

	if err := database.EnsureProxySlots(2); err != nil {
		t.Fatal(err)
	}
	slots, err := database.ListProxySlots()
	if err != nil {
		t.Fatal(err)
	}
	if len(slots) != 2 {
		t.Fatalf("expected 2 slots, got %d", len(slots))
	}
	passwords := map[string]bool{}
	for _, slot := range slots {
		if slot.Status != "active" {
			t.Fatalf("expected active slot, got %q", slot.Status)
		}
		if slot.Username == "" || slot.Password == "" {
			t.Fatalf("slot missing credentials: %#v", slot)
		}
		if passwords[slot.Password] {
			t.Fatalf("duplicate slot password %q", slot.Password)
		}
		passwords[slot.Password] = true
		if slot.AccountID != nil || slot.AccountTag != "" {
			t.Fatalf("new slot should wait for scheduler health binding: %#v", slot)
		}
	}

	if err := database.EnsureProxySlots(1); err != nil {
		t.Fatal(err)
	}
	slots, err = database.ListProxySlots()
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	disabled := 0
	for _, slot := range slots {
		switch slot.Status {
		case "active":
			active++
		case "disabled":
			disabled++
			if slot.AccountID != nil {
				t.Fatalf("disabled slot should be unbound: %#v", slot)
			}
		}
	}
	if active != 1 || disabled != 1 {
		t.Fatalf("expected 1 active and 1 disabled slot, got active=%d disabled=%d", active, disabled)
	}

	var activeSlotID int64
	for _, slot := range slots {
		if slot.Status == "active" {
			activeSlotID = slot.ID
			break
		}
	}
	if activeSlotID == 0 {
		t.Fatal("expected an active slot before delete")
	}
	boundID := accountIDs[0]
	if err := database.AssignProxySlotAccount(activeSlotID, boundID); err != nil {
		t.Fatal(err)
	}
	if err := database.DeleteAccount(boundID); err != nil {
		t.Fatal(err)
	}
	slots, err = database.ListProxySlots()
	if err != nil {
		t.Fatal(err)
	}
	for _, slot := range slots {
		if slot.AccountID != nil && *slot.AccountID == boundID {
			t.Fatalf("slot still references deleted account: %#v", slot)
		}
	}
}

func TestClientUsageAccumulatesByClientIP(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := database.Migrate(); err != nil {
		t.Fatal(err)
	}

	if err := database.AddClientUsage("192.0.2.10", "pf-001", "warp-001", 100, 200); err != nil {
		t.Fatal(err)
	}
	if err := database.AddClientUsage("192.0.2.10", "pf-002", "warp-002", 50, 70); err != nil {
		t.Fatal(err)
	}

	clients, err := database.ListClientUsage(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(clients) != 1 {
		t.Fatalf("expected one client usage row, got %d", len(clients))
	}
	got := clients[0]
	if got.ClientIP != "192.0.2.10" || got.Username != "pf-002" || got.AccountTag != "warp-002" {
		t.Fatalf("latest client identity not recorded: %#v", got)
	}
	if got.TotalUp != 150 || got.TotalDown != 270 || got.HitCount != 2 {
		t.Fatalf("traffic not accumulated: %#v", got)
	}
}

func databaseMustNextTag(t *testing.T, database *DB) string {
	t.Helper()
	tag, err := database.NextTag()
	if err != nil {
		t.Fatal(err)
	}
	return tag
}
