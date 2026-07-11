package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/zpooi/ProxyForge/backend/internal/models"
)

//go:embed schema.sql
var schemaSQL string

type DB struct {
	conn *sql.DB
}

func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	if err := conn.Ping(); err != nil {
		return nil, err
	}
	return &DB{conn: conn}, nil
}

func (d *DB) Close() error { return d.conn.Close() }

func (d *DB) Migrate() error {
	if _, err := d.conn.Exec(schemaSQL); err != nil {
		return err
	}
	for _, col := range []struct {
		name string
		ddl  string
	}{
		{name: "client_id", ddl: "ALTER TABLE warp_accounts ADD COLUMN client_id TEXT"},
		{name: "access_token", ddl: "ALTER TABLE warp_accounts ADD COLUMN access_token TEXT"},
		{name: "device_id", ddl: "ALTER TABLE warp_accounts ADD COLUMN device_id TEXT"},
		{name: "license_key", ddl: "ALTER TABLE warp_accounts ADD COLUMN license_key TEXT"},
		{name: "masque_private_key", ddl: "ALTER TABLE warp_accounts ADD COLUMN masque_private_key TEXT"},
		{name: "masque_endpoint_pub_key", ddl: "ALTER TABLE warp_accounts ADD COLUMN masque_endpoint_pub_key TEXT"},
		{name: "masque_endpoint_v4", ddl: "ALTER TABLE warp_accounts ADD COLUMN masque_endpoint_v4 TEXT"},
		{name: "masque_endpoint_v6", ddl: "ALTER TABLE warp_accounts ADD COLUMN masque_endpoint_v6 TEXT"},
		{name: "last_country", ddl: "ALTER TABLE warp_accounts ADD COLUMN last_country TEXT"},
	} {
		if err := d.ensureColumn("warp_accounts", col.name, col.ddl); err != nil {
			return err
		}
	}
	if err := d.ensureColumn("account_tests", "country", "ALTER TABLE account_tests ADD COLUMN country TEXT"); err != nil {
		return err
	}
	if err := d.ensureColumn("proxy_slots", "probe_failures", "ALTER TABLE proxy_slots ADD COLUMN probe_failures INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := d.ensureColumn("proxy_slots", "pinned_public_ip", "ALTER TABLE proxy_slots ADD COLUMN pinned_public_ip TEXT"); err != nil {
		return err
	}
	if err := d.ensureColumn("proxy_slots", "ip_drift_failures", "ALTER TABLE proxy_slots ADD COLUMN ip_drift_failures INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if _, err := d.conn.Exec(`UPDATE proxy_slots
		SET pinned_public_ip = COALESCE((SELECT last_public_ip FROM warp_accounts WHERE warp_accounts.id = proxy_slots.account_id), '')
		WHERE account_id IS NOT NULL AND COALESCE(pinned_public_ip, '') = ''`); err != nil {
		return err
	}
	return nil
}

func (d *DB) Conn() *sql.DB { return d.conn }

func (d *DB) ensureColumn(table, column, ddl string) error {
	rows, err := d.conn.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = d.conn.Exec(ddl)
	return err
}

// ---------- settings ----------

func (d *DB) GetSetting(key string) (string, bool, error) {
	var v string
	err := d.conn.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (d *DB) SetSetting(key, value string) error {
	_, err := d.conn.Exec("INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

func (d *DB) AllSettings() (map[string]string, error) {
	rows, err := d.conn.Query("SELECT key, value FROM settings")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, rows.Err()
}

// ---------- proxy slots ----------

func (d *DB) EnsureProxySlots(target int) error {
	if target <= 0 {
		target = DefaultProxyIPCount
	}
	slots, err := d.ListProxySlots()
	if err != nil {
		return err
	}
	existingUsernames := map[string]bool{}
	for _, s := range slots {
		existingUsernames[s.Username] = true
	}
	for len(slots) < target {
		username := nextSlotUsername(existingUsernames)
		password, err := randomPassword()
		if err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339)
		res, err := d.conn.Exec(`INSERT INTO proxy_slots(username, password, status, created_at, updated_at)
			VALUES(?, ?, 'active', ?, ?)`, username, password, now, now)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		slots = append(slots, &models.ProxySlot{ID: id, Username: username, Password: password, Status: "active"})
		existingUsernames[username] = true
	}
	now := time.Now().UTC().Format(time.RFC3339)
	for i, slot := range slots {
		status := "active"
		if i >= target {
			status = "disabled"
		}
		if slot.Status == status {
			continue
		}
		if status == "disabled" {
			if _, err := d.conn.Exec(`UPDATE proxy_slots SET status = ?, account_id = NULL, last_error = '',
				probe_failures = 0, pinned_public_ip = '', ip_drift_failures = 0, updated_at = ? WHERE id = ?`,
				status, now, slot.ID); err != nil {
				return err
			}
		} else {
			if _, err := d.conn.Exec(`UPDATE proxy_slots SET status = ?, last_error = '', updated_at = ? WHERE id = ?`,
				status, now, slot.ID); err != nil {
				return err
			}
		}
		slot.Status = status
		if status == "disabled" {
			slot.AccountID = nil
		}
	}
	return nil
}

func (d *DB) ListProxySlots() ([]*models.ProxySlot, error) {
	rows, err := d.conn.Query(`SELECT
		s.id, s.username, s.password, s.account_id, s.status, s.last_error, s.probe_failures,
		s.pinned_public_ip, s.ip_drift_failures, s.created_at, s.updated_at,
		a.tag, a.status, a.last_public_ip, a.last_country, a.last_latency_ms, a.last_speed_bps, a.last_packet_loss, a.last_score, a.is_ip_keeper,
		(SELECT t.error FROM account_tests t WHERE t.account_id = a.id ORDER BY t.id DESC LIMIT 1)
		FROM proxy_slots s
		LEFT JOIN warp_accounts a ON a.id = s.account_id
		ORDER BY s.username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.ProxySlot
	for rows.Next() {
		var s models.ProxySlot
		var accountID sql.NullInt64
		var lastErr, createdAt, updatedAt sql.NullString
		var tag, accountStatus, publicIP, pinnedPublicIP, country, testErr sql.NullString
		var latency, speed, keeper sql.NullInt64
		var probeFailures, ipDriftFailures sql.NullInt64
		var packetLoss, score sql.NullFloat64
		if err := rows.Scan(&s.ID, &s.Username, &s.Password, &accountID, &s.Status, &lastErr, &probeFailures,
			&pinnedPublicIP, &ipDriftFailures, &createdAt, &updatedAt,
			&tag, &accountStatus, &publicIP, &country, &latency, &speed, &packetLoss, &score, &keeper, &testErr); err != nil {
			return nil, err
		}
		if accountID.Valid {
			id := accountID.Int64
			s.AccountID = &id
		}
		s.LastError = lastErr.String
		s.ProbeFailures = int(probeFailures.Int64)
		s.PinnedPublicIP = pinnedPublicIP.String
		s.IPDriftFailures = int(ipDriftFailures.Int64)
		if createdAt.Valid {
			s.CreatedAt, _ = time.Parse(time.RFC3339, createdAt.String)
		}
		if updatedAt.Valid {
			s.UpdatedAt, _ = time.Parse(time.RFC3339, updatedAt.String)
		}
		s.AccountTag = tag.String
		s.AccountStatus = accountStatus.String
		s.PublicIP = publicIP.String
		s.Country = country.String
		s.LatencyMs = int(latency.Int64)
		s.SpeedBps = int(speed.Int64)
		s.PacketLoss = packetLoss.Float64
		s.Score = score.Float64
		s.IsKeeper = keeper.Int64 == 1
		out = append(out, &s)
	}
	return out, rows.Err()
}

func (d *DB) AssignProxySlotAccount(slotID, accountID int64) error {
	_, err := d.conn.Exec(`UPDATE proxy_slots SET account_id = ?,
		pinned_public_ip = COALESCE((SELECT last_public_ip FROM warp_accounts WHERE id = ?), ''),
		last_error = '', probe_failures = 0, ip_drift_failures = 0, updated_at = ? WHERE id = ?`,
		accountID, accountID, time.Now().UTC().Format(time.RFC3339), slotID)
	return err
}

func (d *DB) MarkProxySlotError(slotID int64, msg string) error {
	_, err := d.conn.Exec(`UPDATE proxy_slots SET last_error = ?,
		probe_failures = CASE WHEN ? = '' THEN 0 ELSE probe_failures END,
		ip_drift_failures = CASE WHEN ? = '' THEN 0 ELSE ip_drift_failures END,
		updated_at = ? WHERE id = ?`,
		msg, msg, msg, time.Now().UTC().Format(time.RFC3339), slotID)
	return err
}

func (d *DB) SetProxySlotPinnedIP(slotID int64, ip string) error {
	_, err := d.conn.Exec(`UPDATE proxy_slots SET pinned_public_ip = ?, ip_drift_failures = 0, updated_at = ? WHERE id = ?`,
		ip, time.Now().UTC().Format(time.RFC3339), slotID)
	return err
}

func (d *DB) MarkProxySlotIPDrift(slotID int64, msg string) error {
	_, err := d.conn.Exec(`UPDATE proxy_slots SET ip_drift_failures = ip_drift_failures + 1, last_error = ?, updated_at = ? WHERE id = ?`,
		msg, time.Now().UTC().Format(time.RFC3339), slotID)
	return err
}

func (d *DB) MarkProxySlotProbeFailure(slotID int64, msg string) error {
	_, err := d.conn.Exec(`UPDATE proxy_slots SET probe_failures = probe_failures + 1, last_error = ?, updated_at = ? WHERE id = ?`,
		msg, time.Now().UTC().Format(time.RFC3339), slotID)
	return err
}

func (d *DB) UnassignProxySlot(slotID int64, msg string) error {
	_, err := d.conn.Exec(`UPDATE proxy_slots SET account_id = NULL, last_error = ?, probe_failures = 0,
		pinned_public_ip = '', ip_drift_failures = 0, updated_at = ? WHERE id = ?`,
		msg, time.Now().UTC().Format(time.RFC3339), slotID)
	return err
}

func (d *DB) assignEmptyProxySlots(slots []*models.ProxySlot) error {
	accounts, err := d.ListActiveAccounts()
	if err != nil {
		return err
	}
	assigned := map[int64]bool{}
	for _, s := range slots {
		if s.AccountID != nil {
			assigned[*s.AccountID] = true
		}
	}
	nextAccount := 0
	for _, s := range slots {
		if s.AccountID != nil || s.Status != "active" {
			continue
		}
		for nextAccount < len(accounts) && assigned[accounts[nextAccount].ID] {
			nextAccount++
		}
		if nextAccount >= len(accounts) {
			break
		}
		account := accounts[nextAccount]
		if err := d.AssignProxySlotAccount(s.ID, account.ID); err != nil {
			return err
		}
		assigned[account.ID] = true
		nextAccount++
	}
	return nil
}

func nextSlotUsername(existing map[string]bool) string {
	for i := 1; ; i++ {
		username := fmt.Sprintf("pf-%03d", i)
		if !existing[username] {
			return username
		}
	}
}

func randomPassword() (string, error) {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// ---------- users ----------

func (d *DB) GetUserByUsername(username string) (*models.User, error) {
	var u models.User
	var mcp int
	var createdAt string
	err := d.conn.QueryRow("SELECT id, username, password_hash, must_change_password, created_at FROM users WHERE username = ?", username).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &mcp, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.MustChangePassword = mcp == 1
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	return &u, nil
}

func (d *DB) CreateUser(username, passwordHash string) error {
	_, err := d.conn.Exec("INSERT INTO users(username, password_hash, must_change_password, created_at) VALUES(?, ?, 0, ?)", username, passwordHash, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (d *DB) UpdatePassword(userID int64, passwordHash string) error {
	_, err := d.conn.Exec("UPDATE users SET password_hash = ?, must_change_password = 0 WHERE id = ?", passwordHash, userID)
	return err
}

func (d *DB) UpdateUserCredentials(userID int64, username, passwordHash string) error {
	_, err := d.conn.Exec("UPDATE users SET username = ?, password_hash = ?, must_change_password = 0 WHERE id = ?",
		username, passwordHash, userID)
	return err
}

// ---------- sessions ----------

func (d *DB) CreateSession(token string, userID int64, ttl time.Duration) error {
	expires := time.Now().Add(ttl).UTC().Format(time.RFC3339)
	_, err := d.conn.Exec("INSERT INTO sessions(token, user_id, expires_at) VALUES(?, ?, ?)", token, userID, expires)
	return err
}

func (d *DB) GetSession(token string) (*models.User, error) {
	var u models.User
	var mcp int
	var expires string
	var createdAt string
	err := d.conn.QueryRow(`SELECT u.id, u.username, u.password_hash, u.must_change_password, u.created_at, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id WHERE s.token = ?`, token).
		Scan(&u.ID, &u.Username, &u.PasswordHash, &mcp, &createdAt, &expires)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	u.MustChangePassword = mcp == 1
	u.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	exp, _ := time.Parse(time.RFC3339, expires)
	if time.Now().UTC().After(exp) {
		_, _ = d.conn.Exec("DELETE FROM sessions WHERE token = ?", token)
		return nil, nil
	}
	return &u, nil
}

func (d *DB) DeleteSession(token string) error {
	_, err := d.conn.Exec("DELETE FROM sessions WHERE token = ?", token)
	return err
}

// ---------- accounts ----------

func (d *DB) ListAccounts() ([]*models.Account, error) {
	rows, err := d.conn.Query(`SELECT id, tag, directory, status, private_key, client_id, access_token, device_id, license_key, peer_public_key,
		local_address_v4, local_address_v6, endpoint_host, endpoint_port, mtu, listen_port,
		masque_private_key, masque_endpoint_pub_key, masque_endpoint_v4, masque_endpoint_v6,
		last_public_ip, last_colo, last_country, last_latency_ms, last_speed_bps, last_packet_loss, last_score,
		last_tested_at, traffic_up, traffic_down, is_ip_keeper, created_at, disabled_reason
		FROM warp_accounts ORDER BY tag`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) ListActiveAccounts() ([]*models.Account, error) {
	rows, err := d.conn.Query(`SELECT id, tag, directory, status, private_key, client_id, access_token, device_id, license_key, peer_public_key,
		local_address_v4, local_address_v6, endpoint_host, endpoint_port, mtu, listen_port,
		masque_private_key, masque_endpoint_pub_key, masque_endpoint_v4, masque_endpoint_v6,
		last_public_ip, last_colo, last_country, last_latency_ms, last_speed_bps, last_packet_loss, last_score,
		last_tested_at, traffic_up, traffic_down, is_ip_keeper, created_at, disabled_reason
		FROM warp_accounts WHERE status = 'active' ORDER BY tag`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) GetAccount(id int64) (*models.Account, error) {
	row := d.conn.QueryRow(`SELECT id, tag, directory, status, private_key, client_id, access_token, device_id, license_key, peer_public_key,
		local_address_v4, local_address_v6, endpoint_host, endpoint_port, mtu, listen_port,
		masque_private_key, masque_endpoint_pub_key, masque_endpoint_v4, masque_endpoint_v6,
		last_public_ip, last_colo, last_country, last_latency_ms, last_speed_bps, last_packet_loss, last_score,
		last_tested_at, traffic_up, traffic_down, is_ip_keeper, created_at, disabled_reason
		FROM warp_accounts WHERE id = ?`, id)
	return scanAccountRow(row)
}

func (d *DB) GetAccountByTag(tag string) (*models.Account, error) {
	row := d.conn.QueryRow(`SELECT id, tag, directory, status, private_key, client_id, access_token, device_id, license_key, peer_public_key,
		local_address_v4, local_address_v6, endpoint_host, endpoint_port, mtu, listen_port,
		masque_private_key, masque_endpoint_pub_key, masque_endpoint_v4, masque_endpoint_v6,
		last_public_ip, last_colo, last_country, last_latency_ms, last_speed_bps, last_packet_loss, last_score,
		last_tested_at, traffic_up, traffic_down, is_ip_keeper, created_at, disabled_reason
		FROM warp_accounts WHERE tag = ?`, tag)
	return scanAccountRow(row)
}

func (d *DB) GetAccountByListenPort(port int) (*models.Account, error) {
	row := d.conn.QueryRow(`SELECT id, tag, directory, status, private_key, client_id, access_token, device_id, license_key, peer_public_key,
		local_address_v4, local_address_v6, endpoint_host, endpoint_port, mtu, listen_port,
		masque_private_key, masque_endpoint_pub_key, masque_endpoint_v4, masque_endpoint_v6,
		last_public_ip, last_colo, last_country, last_latency_ms, last_speed_bps, last_packet_loss, last_score,
		last_tested_at, traffic_up, traffic_down, is_ip_keeper, created_at, disabled_reason
		FROM warp_accounts WHERE listen_port = ?`, port)
	return scanAccountRow(row)
}

func (d *DB) InsertAccount(a *models.Account) error {
	res, err := d.conn.Exec(`INSERT INTO warp_accounts
		(tag, directory, status, private_key, client_id, access_token, device_id, license_key, peer_public_key, local_address_v4, local_address_v6,
		 endpoint_host, endpoint_port, mtu, listen_port, masque_private_key, masque_endpoint_pub_key, masque_endpoint_v4, masque_endpoint_v6, created_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Tag, a.Directory, a.Status, a.PrivateKey, a.ClientID, a.AccessToken, a.DeviceID, a.LicenseKey, a.PeerPublicKey, a.LocalAddressV4, a.LocalAddressV6,
		a.EndpointHost, a.EndpointPort, a.MTU, a.ListenPort, a.MasquePrivateKey, a.MasqueEndpointPubKey, a.MasqueEndpointV4, a.MasqueEndpointV6,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	a.ID, _ = res.LastInsertId()
	return nil
}

func (d *DB) UpdateAccountStatus(id int64, status, reason string) error {
	_, err := d.conn.Exec("UPDATE warp_accounts SET status = ?, disabled_reason = ? WHERE id = ?", status, reason, id)
	return err
}

func (d *DB) UpdateAccountListenPort(id int64, port int) error {
	_, err := d.conn.Exec("UPDATE warp_accounts SET listen_port = ? WHERE id = ?", port, id)
	return err
}

func (d *DB) UpdateAccountClientID(id int64, clientID string) error {
	_, err := d.conn.Exec("UPDATE warp_accounts SET client_id = ? WHERE id = ?", clientID, id)
	return err
}

func (d *DB) UpdateAccountMasque(id int64, privateKey, endpointPubKey, endpointV4, endpointV6, addressV4, addressV6 string) error {
	_, err := d.conn.Exec(`UPDATE warp_accounts SET
		masque_private_key = ?,
		masque_endpoint_pub_key = ?,
		masque_endpoint_v4 = ?,
		masque_endpoint_v6 = ?,
		local_address_v4 = COALESCE(NULLIF(?, ''), local_address_v4),
		local_address_v6 = COALESCE(NULLIF(?, ''), local_address_v6)
		WHERE id = ?`,
		privateKey, endpointPubKey, endpointV4, endpointV6, addressV4, addressV6, id)
	return err
}

func (d *DB) UpdateAccountTestResult(id int64, ip, colo, country string, latencyMs, speedBps int, packetLoss, score float64) error {
	_, err := d.conn.Exec(`UPDATE warp_accounts SET last_public_ip = ?, last_colo = ?, last_country = ?, last_latency_ms = ?,
		last_speed_bps = ?, last_packet_loss = ?, last_score = ?, last_tested_at = ? WHERE id = ?`,
		ip, colo, country, latencyMs, speedBps, packetLoss, score, time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return err
	}
	_, err = d.conn.Exec(`INSERT INTO account_tests(account_id, tested_at, public_ip, colo, country, latency_ms, speed_bps, packet_loss, score)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, time.Now().UTC().Format(time.RFC3339), ip, colo, country, latencyMs, speedBps, packetLoss, score)
	return err
}

func (d *DB) UpdateAccountRealtimeProbe(id int64, ip, colo, country string, latencyMs int) error {
	_, err := d.conn.Exec(`UPDATE warp_accounts SET last_public_ip = ?, last_colo = ?, last_country = ?, last_latency_ms = ?,
		last_packet_loss = 0, last_tested_at = ? WHERE id = ?`,
		ip, colo, country, latencyMs, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func (d *DB) UpdateAccountTestError(id int64, errMsg string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.conn.Exec(`UPDATE warp_accounts SET last_tested_at = ? WHERE id = ?`, now, id)
	if err != nil {
		return err
	}
	if _, err = d.conn.Exec(`INSERT INTO account_tests(account_id, tested_at, error) VALUES(?, ?, ?)`,
		id, now, errMsg); err != nil {
		return err
	}
	failures, err := d.recentConsecutiveErrors(id, 3)
	if err != nil {
		return err
	}
	if failures >= 3 {
		_, err = d.conn.Exec(`UPDATE warp_accounts SET status = 'error', disabled_reason = ? WHERE id = ?`,
			"3 consecutive test failures: "+errMsg, id)
	}
	return err
}

func (d *DB) recentConsecutiveErrors(accountID int64, limit int) (int, error) {
	rows, err := d.conn.Query(`SELECT error FROM account_tests WHERE account_id = ? ORDER BY id DESC LIMIT ?`, accountID, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var errMsg sql.NullString
		if err := rows.Scan(&errMsg); err != nil {
			return 0, err
		}
		if !errMsg.Valid || errMsg.String == "" {
			break
		}
		count++
	}
	return count, rows.Err()
}

func (d *DB) AddTraffic(inboundTag string, upDelta, downDelta int64) error {
	// inboundTag 形如 "mixed-warp-001"，对应 account tag = warp-001
	tag := strings.TrimPrefix(inboundTag, "mixed-")
	if tag == inboundTag {
		return nil
	}
	return d.AddTrafficByTag(tag, upDelta, downDelta)
}

// AddTrafficByTag 按账号 tag 直接累加上/下行字节。进程内代理运行时用它入账。
func (d *DB) AddTrafficByTag(tag string, upDelta, downDelta int64) error {
	_, err := d.conn.Exec(`UPDATE warp_accounts SET traffic_up = traffic_up + ?, traffic_down = traffic_down + ? WHERE tag = ?`,
		upDelta, downDelta, tag)
	return err
}

// AssignListenPorts 按 tag 升序给所有 active 账号从 portStart 起连续分配本地监听端口，
// 仅在端口变化时写库。返回分配后账号数。替代旧 generator 里的端口分配职责。
func (d *DB) AssignListenPorts(portStart int) (int, error) {
	if portStart <= 0 {
		portStart = 20001
	}
	accounts, err := d.ListActiveAccounts()
	if err != nil {
		return 0, err
	}
	for i, a := range accounts {
		port := portStart + i
		if a.ListenPort != port {
			if err := d.UpdateAccountListenPort(a.ID, port); err != nil {
				return 0, err
			}
		}
	}
	return len(accounts), nil
}

func (d *DB) SetIPPoolCurrent(ip string, upBps, downBps int64) error {
	_, err := d.conn.Exec(`INSERT INTO ip_pool(public_ip, current_up_bps, current_down_bps, last_seen_at)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(public_ip) DO UPDATE SET current_up_bps = excluded.current_up_bps,
			current_down_bps = excluded.current_down_bps, last_seen_at = excluded.last_seen_at`,
		ip, upBps, downBps, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (d *DB) AddIPPoolTraffic(ip string, upDelta, downDelta int64) error {
	_, err := d.conn.Exec(`UPDATE ip_pool SET total_up = total_up + ?, total_down = total_down + ? WHERE public_ip = ?`,
		upDelta, downDelta, ip)
	return err
}

func (d *DB) AddClientUsage(clientIP, username, accountTag string, upDelta, downDelta int64) error {
	clientIP = strings.TrimSpace(clientIP)
	if clientIP == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.conn.Exec(`INSERT INTO proxy_clients(client_ip, username, account_tag, total_up, total_down, hit_count, first_seen_at, last_seen_at)
		VALUES(?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(client_ip) DO UPDATE SET
			username = excluded.username,
			account_tag = excluded.account_tag,
			total_up = total_up + excluded.total_up,
			total_down = total_down + excluded.total_down,
			hit_count = hit_count + 1,
			last_seen_at = excluded.last_seen_at`,
		clientIP, strings.TrimSpace(username), strings.TrimSpace(accountTag), upDelta, downDelta, now, now)
	return err
}

func (d *DB) ListClientUsage(limit int) ([]*models.ProxyClientUsage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := d.conn.Query(`SELECT client_ip, username, account_tag, total_up, total_down, hit_count, first_seen_at, last_seen_at
		FROM proxy_clients ORDER BY last_seen_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*models.ProxyClientUsage
	for rows.Next() {
		var item models.ProxyClientUsage
		var firstSeen, lastSeen string
		if err := rows.Scan(&item.ClientIP, &item.Username, &item.AccountTag, &item.TotalUp, &item.TotalDown,
			&item.HitCount, &firstSeen, &lastSeen); err != nil {
			return nil, err
		}
		item.FirstSeen, _ = time.Parse(time.RFC3339, firstSeen)
		item.LastSeen, _ = time.Parse(time.RFC3339, lastSeen)
		out = append(out, &item)
	}
	return out, rows.Err()
}

func (d *DB) SetIPKeeper(ip string, accountID int64) error {
	_, err := d.conn.Exec(`INSERT INTO ip_pool(public_ip, keeper_account_id, last_seen_at)
		VALUES(?, ?, ?)
		ON CONFLICT(public_ip) DO UPDATE SET keeper_account_id = excluded.keeper_account_id, last_seen_at = excluded.last_seen_at`,
		ip, accountID, time.Now().UTC().Format(time.RFC3339))
	_, _ = d.conn.Exec("UPDATE warp_accounts SET is_ip_keeper = CASE WHEN id = ? THEN 1 ELSE 0 END WHERE last_public_ip = ?", accountID, ip)
	return err
}

func (d *DB) ClearIPKeeperExcept(ip string, keeperID int64) error {
	_, err := d.conn.Exec(`UPDATE warp_accounts SET is_ip_keeper = 0 WHERE last_public_ip = ? AND id != ?`, ip, keeperID)
	return err
}

// NextTag 基于数据库里已有的 warp-NNN 账号，返回下一个可用的 tag。
func (d *DB) NextTag() (string, error) {
	rows, err := d.conn.Query(`SELECT tag FROM warp_accounts`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	maxNum := 0
	for rows.Next() {
		var tag string
		if err := rows.Scan(&tag); err != nil {
			return "", err
		}
		var n int
		if _, err := fmt.Sscanf(tag, "warp-%d", &n); err == nil && n > maxNum {
			maxNum = n
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("warp-%03d", maxNum+1), nil
}

func (d *DB) ListIPPool() ([]*models.IPPoolEntry, error) {
	rows, err := d.conn.Query(`SELECT id, public_ip, keeper_account_id, total_up, total_down, current_up_bps, current_down_bps, last_seen_at FROM ip_pool ORDER BY public_ip`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.IPPoolEntry
	for rows.Next() {
		var e models.IPPoolEntry
		var keeper sql.NullInt64
		var lastSeen sql.NullString
		if err := rows.Scan(&e.ID, &e.PublicIP, &keeper, &e.TotalUp, &e.TotalDown, &e.CurrentUpBps, &e.CurrentDownBps, &lastSeen); err != nil {
			return nil, err
		}
		if keeper.Valid {
			id := keeper.Int64
			e.KeeperAccountID = &id
		}
		if lastSeen.Valid {
			t, _ := time.Parse(time.RFC3339, lastSeen.String)
			e.LastSeenAt = &t
		}
		out = append(out, &e)
	}
	return out, rows.Err()
}

// AddTrafficSample 记录一条整体吞吐采样点（字节/秒）。由 trafficLoop 定期调用，
// 作为仪表盘吞吐时间序列的服务端数据源，刷新页面或重启进程都不丢。
func (d *DB) AddTrafficSample(upBps, downBps int64) error {
	_, err := d.conn.Exec(`INSERT INTO traffic_samples(sampled_at, up_bps, down_bps) VALUES(?, ?, ?)`,
		time.Now().UTC().Format(time.RFC3339), upBps, downBps)
	return err
}

// PruneTrafficSamples 删除早于 maxAge 的采样，控制表大小。
func (d *DB) PruneTrafficSamples(maxAge time.Duration) error {
	cutoff := time.Now().UTC().Add(-maxAge).Format(time.RFC3339)
	_, err := d.conn.Exec(`DELETE FROM traffic_samples WHERE sampled_at < ?`, cutoff)
	return err
}

// ListTrafficSamples 返回近 since 时间内的吞吐采样，按时间升序，供前端画图。
func (d *DB) ListTrafficSamples(since time.Duration) ([]*models.TrafficSample, error) {
	cutoff := time.Now().UTC().Add(-since).Format(time.RFC3339)
	rows, err := d.conn.Query(`SELECT sampled_at, up_bps, down_bps FROM traffic_samples
		WHERE sampled_at >= ? ORDER BY sampled_at ASC`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.TrafficSample
	for rows.Next() {
		var s models.TrafficSample
		var at string
		if err := rows.Scan(&at, &s.UpBps, &s.DownBps); err != nil {
			return nil, err
		}
		s.SampledAt, _ = time.Parse(time.RFC3339, at)
		out = append(out, &s)
	}
	return out, rows.Err()
}

func (d *DB) DeleteAccount(id int64) error {
	tx, err := d.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`UPDATE proxy_slots SET account_id = NULL, last_error = 'bound account deleted',
		probe_failures = 0, pinned_public_ip = '', ip_drift_failures = 0, updated_at = ? WHERE account_id = ?`,
		now, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE ip_pool SET keeper_account_id = NULL WHERE keeper_account_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM account_tests WHERE account_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM warp_accounts WHERE id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

// CountAccounts 返回账号总数。用于自动补齐时判断是否达到目标数量。
func (d *DB) CountAccounts() (int, error) {
	var n int
	err := d.conn.QueryRow("SELECT COUNT(*) FROM warp_accounts").Scan(&n)
	return n, err
}

// ---------- schedule runs ----------

func (d *DB) StartRun(kind string) (int64, error) {
	res, err := d.conn.Exec(`INSERT INTO schedule_runs(started_at, kind, status) VALUES(?, ?, 'running')`,
		time.Now().UTC().Format(time.RFC3339), kind)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) FinishRun(id int64, status, detail string, kept, disabled *int) error {
	_, err := d.conn.Exec(`UPDATE schedule_runs SET finished_at = ?, status = ?, detail = ?, accounts_kept = ?, accounts_disabled = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), status, detail, kept, disabled, id)
	return err
}

func (d *DB) ListRuns(limit int) ([]*models.ScheduleRun, error) {
	rows, err := d.conn.Query(`SELECT id, started_at, finished_at, kind, status, detail, accounts_kept, accounts_disabled
		FROM schedule_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.ScheduleRun
	for rows.Next() {
		var r models.ScheduleRun
		var finished sql.NullString
		var detail sql.NullString
		var kept, disabled sql.NullInt64
		var started string
		if err := rows.Scan(&r.ID, &started, &finished, &r.Kind, &r.Status, &detail, &kept, &disabled); err != nil {
			return nil, err
		}
		r.StartedAt, _ = time.Parse(time.RFC3339, started)
		if finished.Valid {
			t, _ := time.Parse(time.RFC3339, finished.String)
			r.FinishedAt = &t
		}
		if detail.Valid {
			r.Detail = detail.String
		}
		if kept.Valid {
			k := int(kept.Int64)
			r.AccountsKept = &k
		}
		if disabled.Valid {
			dd := int(disabled.Int64)
			r.AccountsDisabled = &dd
		}
		out = append(out, &r)
	}
	return out, rows.Err()
}

// ListAccountTests 返回某账号最近 limit 条测试历史（按时间倒序）。
func (d *DB) ListAccountTests(accountID int64, limit int) ([]*models.AccountTest, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := d.conn.Query(`SELECT id, account_id, tested_at, public_ip, colo, country, latency_ms, speed_bps, packet_loss, score, error
		FROM account_tests WHERE account_id = ? ORDER BY id DESC LIMIT ?`, accountID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.AccountTest
	for rows.Next() {
		var t models.AccountTest
		var tested string
		var publicIP, colo, country, errMsg sql.NullString
		var latency, speed sql.NullInt64
		var loss, score sql.NullFloat64
		if err := rows.Scan(&t.ID, &t.AccountID, &tested, &publicIP, &colo, &country, &latency, &speed, &loss, &score, &errMsg); err != nil {
			return nil, err
		}
		t.TestedAt, _ = time.Parse(time.RFC3339, tested)
		t.PublicIP = publicIP.String
		t.Colo = colo.String
		t.Country = country.String
		t.LatencyMs = int(latency.Int64)
		t.SpeedBps = int(speed.Int64)
		t.PacketLoss = loss.Float64
		t.Score = score.Float64
		t.Error = errMsg.String
		out = append(out, &t)
	}
	return out, rows.Err()
}

// ---------- agent nodes ----------

// UpsertAgentNode 记录/更新一个远程 agent 节点的最近上报信息。node_id 稳定，
// agent 每次重连都带同一个，靠它幂等 upsert；enabled 与 created_at 首次写入后保留。
func (d *DB) UpsertAgentNode(nodeID, name, publicIP, country, colo string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := d.conn.Exec(`INSERT INTO agent_nodes
		(node_id, name, public_ip, country, colo, enabled, last_seen_at, created_at)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?)
		ON CONFLICT(node_id) DO UPDATE SET
			name = excluded.name,
			public_ip = excluded.public_ip,
			country = excluded.country,
			colo = excluded.colo,
			last_seen_at = excluded.last_seen_at`,
		nodeID, name, publicIP, country, colo, now, now)
	return err
}

// TouchAgentNode 只刷新 last_seen_at，用于心跳保活，避免频繁重写全部字段。
func (d *DB) TouchAgentNode(nodeID string) error {
	_, err := d.conn.Exec(`UPDATE agent_nodes SET last_seen_at = ? WHERE node_id = ?`,
		time.Now().UTC().Format(time.RFC3339), nodeID)
	return err
}

func (d *DB) ListAgentNodes() ([]*models.AgentNode, error) {
	rows, err := d.conn.Query(`SELECT id, node_id, name, public_ip, country, colo, enabled, last_seen_at, created_at
		FROM agent_nodes ORDER BY country, node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*models.AgentNode
	for rows.Next() {
		var n models.AgentNode
		var name, publicIP, country, colo, lastSeen, createdAt sql.NullString
		var enabled int
		if err := rows.Scan(&n.ID, &n.NodeID, &name, &publicIP, &country, &colo, &enabled, &lastSeen, &createdAt); err != nil {
			return nil, err
		}
		n.Name = name.String
		n.PublicIP = publicIP.String
		n.Country = country.String
		n.Colo = colo.String
		n.Enabled = enabled == 1
		if lastSeen.Valid && lastSeen.String != "" {
			if t, err := time.Parse(time.RFC3339, lastSeen.String); err == nil {
				n.LastSeenAt = &t
			}
		}
		if createdAt.Valid {
			n.CreatedAt, _ = time.Parse(time.RFC3339, createdAt.String)
		}
		out = append(out, &n)
	}
	return out, rows.Err()
}

func (d *DB) SetAgentNodeEnabled(nodeID string, enabled bool) error {
	v := 0
	if enabled {
		v = 1
	}
	_, err := d.conn.Exec(`UPDATE agent_nodes SET enabled = ? WHERE node_id = ?`, v, nodeID)
	return err
}

func (d *DB) DeleteAgentNode(nodeID string) error {
	_, err := d.conn.Exec(`DELETE FROM agent_nodes WHERE node_id = ?`, nodeID)
	return err
}

// ---------- helpers ----------

func scanAccount(rows *sql.Rows) (*models.Account, error) {
	var a models.Account
	var lastTested sql.NullString
	var ipAddress4 sql.NullString
	var ipAddress6 sql.NullString
	var publicIP sql.NullString
	var colo sql.NullString
	var country sql.NullString
	var disabledReason sql.NullString
	var clientID sql.NullString
	var accessToken sql.NullString
	var deviceID sql.NullString
	var licenseKey sql.NullString
	var masquePrivateKey sql.NullString
	var masqueEndpointPubKey sql.NullString
	var masqueEndpointV4 sql.NullString
	var masqueEndpointV6 sql.NullString
	var isKeeper int
	var createdAt sql.NullString
	var latencyMs sql.NullInt64
	var speedBps sql.NullInt64
	var packetLoss sql.NullFloat64
	var score sql.NullFloat64
	if err := rows.Scan(&a.ID, &a.Tag, &a.Directory, &a.Status, &a.PrivateKey, &clientID, &accessToken, &deviceID, &licenseKey, &a.PeerPublicKey,
		&ipAddress4, &ipAddress6, &a.EndpointHost, &a.EndpointPort, &a.MTU, &a.ListenPort,
		&masquePrivateKey, &masqueEndpointPubKey, &masqueEndpointV4, &masqueEndpointV6,
		&publicIP, &colo, &country, &latencyMs, &speedBps, &packetLoss, &score,
		&lastTested, &a.TrafficUp, &a.TrafficDown, &isKeeper, &createdAt, &disabledReason); err != nil {
		return nil, err
	}
	a.IsIPKeeper = isKeeper == 1
	a.LastLatencyMs = int(latencyMs.Int64)
	a.LastSpeedBps = int(speedBps.Int64)
	a.LastPacketLoss = packetLoss.Float64
	a.LastScore = score.Float64
	if createdAt.Valid {
		a.CreatedAt, _ = time.Parse(time.RFC3339, createdAt.String)
	}
	if ipAddress4.Valid {
		a.LocalAddressV4 = ipAddress4.String
	}
	if ipAddress6.Valid {
		a.LocalAddressV6 = ipAddress6.String
	}
	if clientID.Valid {
		a.ClientID = clientID.String
	}
	if accessToken.Valid {
		a.AccessToken = accessToken.String
	}
	if deviceID.Valid {
		a.DeviceID = deviceID.String
	}
	if licenseKey.Valid {
		a.LicenseKey = licenseKey.String
	}
	if masquePrivateKey.Valid {
		a.MasquePrivateKey = masquePrivateKey.String
	}
	if masqueEndpointPubKey.Valid {
		a.MasqueEndpointPubKey = masqueEndpointPubKey.String
	}
	if masqueEndpointV4.Valid {
		a.MasqueEndpointV4 = masqueEndpointV4.String
	}
	if masqueEndpointV6.Valid {
		a.MasqueEndpointV6 = masqueEndpointV6.String
	}
	if publicIP.Valid {
		a.LastPublicIP = publicIP.String
	}
	if colo.Valid {
		a.LastColo = colo.String
	}
	if country.Valid {
		a.LastCountry = country.String
	}
	if disabledReason.Valid {
		a.DisabledReason = disabledReason.String
	}
	if lastTested.Valid {
		t, _ := time.Parse(time.RFC3339, lastTested.String)
		a.LastTestedAt = &t
	}
	return &a, nil
}

func scanAccountRow(row *sql.Row) (*models.Account, error) {
	var a models.Account
	var lastTested sql.NullString
	var ipAddress4 sql.NullString
	var ipAddress6 sql.NullString
	var publicIP sql.NullString
	var colo sql.NullString
	var country sql.NullString
	var disabledReason sql.NullString
	var clientID sql.NullString
	var accessToken sql.NullString
	var deviceID sql.NullString
	var licenseKey sql.NullString
	var masquePrivateKey sql.NullString
	var masqueEndpointPubKey sql.NullString
	var masqueEndpointV4 sql.NullString
	var masqueEndpointV6 sql.NullString
	var isKeeper int
	var createdAt sql.NullString
	var latencyMs sql.NullInt64
	var speedBps sql.NullInt64
	var packetLoss sql.NullFloat64
	var score sql.NullFloat64
	err := row.Scan(&a.ID, &a.Tag, &a.Directory, &a.Status, &a.PrivateKey, &clientID, &accessToken, &deviceID, &licenseKey, &a.PeerPublicKey,
		&ipAddress4, &ipAddress6, &a.EndpointHost, &a.EndpointPort, &a.MTU, &a.ListenPort,
		&masquePrivateKey, &masqueEndpointPubKey, &masqueEndpointV4, &masqueEndpointV6,
		&publicIP, &colo, &country, &latencyMs, &speedBps, &packetLoss, &score,
		&lastTested, &a.TrafficUp, &a.TrafficDown, &isKeeper, &createdAt, &disabledReason)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	a.IsIPKeeper = isKeeper == 1
	a.LastLatencyMs = int(latencyMs.Int64)
	a.LastSpeedBps = int(speedBps.Int64)
	a.LastPacketLoss = packetLoss.Float64
	a.LastScore = score.Float64
	if createdAt.Valid {
		a.CreatedAt, _ = time.Parse(time.RFC3339, createdAt.String)
	}
	if ipAddress4.Valid {
		a.LocalAddressV4 = ipAddress4.String
	}
	if ipAddress6.Valid {
		a.LocalAddressV6 = ipAddress6.String
	}
	if clientID.Valid {
		a.ClientID = clientID.String
	}
	if accessToken.Valid {
		a.AccessToken = accessToken.String
	}
	if deviceID.Valid {
		a.DeviceID = deviceID.String
	}
	if licenseKey.Valid {
		a.LicenseKey = licenseKey.String
	}
	if masquePrivateKey.Valid {
		a.MasquePrivateKey = masquePrivateKey.String
	}
	if masqueEndpointPubKey.Valid {
		a.MasqueEndpointPubKey = masqueEndpointPubKey.String
	}
	if masqueEndpointV4.Valid {
		a.MasqueEndpointV4 = masqueEndpointV4.String
	}
	if masqueEndpointV6.Valid {
		a.MasqueEndpointV6 = masqueEndpointV6.String
	}
	if publicIP.Valid {
		a.LastPublicIP = publicIP.String
	}
	if colo.Valid {
		a.LastColo = colo.String
	}
	if country.Valid {
		a.LastCountry = country.String
	}
	if disabledReason.Valid {
		a.DisabledReason = disabledReason.String
	}
	if lastTested.Valid {
		t, _ := time.Parse(time.RFC3339, lastTested.String)
		a.LastTestedAt = &t
	}
	return &a, nil
}

var _ = context.Background
