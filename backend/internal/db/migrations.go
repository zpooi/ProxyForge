package db

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/zpooi/ProxyForge/backend/internal/wgcf"
)

const (
	DefaultProxyIPCount         = 20
	SettingTargetAccountCount   = "target_account_count"
	SettingProxySlotCount       = "proxy_slot_count"
	SettingAutoGeneration       = "auto_generation"
	SettingProxyPassword        = "proxy_password"
	SettingDedupIntervalSeconds = "dedup_interval_seconds"
	SettingProxyListenAddr      = "proxy_listen_addr"
	SettingProxyPort            = "proxy_port"
	SettingWarpTransport        = "warp_transport"
	SettingTunnelIPFamily       = "tunnel_ip_family"
	SettingProxyDNSMode         = "proxy_dns_mode"
	SettingProxyTLS             = "proxy_tls"
	SettingProxyPublicHost      = "proxy_public_host"
)

func (d *DB) SeedDefaultsIfEmpty(projectRoot string) error {
	defaults := map[string]string{
		SettingTargetAccountCount:   fmt.Sprint(DefaultProxyIPCount),
		SettingAutoGeneration:       "on",
		SettingDedupIntervalSeconds: "600",
		SettingProxyListenAddr:      "0.0.0.0",
		SettingProxyPort:            "7843",
		SettingWarpTransport:        "auto",
		SettingTunnelIPFamily:       "ipv4",
		SettingProxyDNSMode:         "tunnel",
		SettingProxyTLS:             "on",
	}
	for k, v := range defaults {
		_, ok, err := d.GetSetting(k)
		if err != nil {
			return err
		}
		if !ok {
			if err := d.SetSetting(k, v); err != nil {
				return err
			}
		}
	}
	// Global aliases (auto/random/stable and agent routes) share this password.
	// Never leave it empty: an empty value used to turn those aliases into an
	// internet-accessible open proxy. Existing empty databases are repaired too.
	proxyPassword, _, err := d.GetSetting(SettingProxyPassword)
	if err != nil {
		return err
	}
	if strings.TrimSpace(proxyPassword) == "" {
		proxyPassword, err = randomPassword()
		if err != nil {
			return fmt.Errorf("generate proxy password: %w", err)
		}
		if err := d.SetSetting(SettingProxyPassword, proxyPassword); err != nil {
			return err
		}
	}
	if _, ok, err := d.GetSetting(SettingProxySlotCount); err != nil {
		return err
	} else if !ok {
		slotCount, _, _ := d.GetSetting(SettingTargetAccountCount)
		if slotCount == "" {
			slotCount = fmt.Sprint(DefaultProxyIPCount)
		}
		if err := d.SetSetting(SettingProxySlotCount, slotCount); err != nil {
			return err
		}
	}

	if err := d.importExistingAccounts(projectRoot); err != nil {
		return err
	}
	return d.EnsureProxySlots(d.targetProxySlots())
}

func (d *DB) CountUsers() (int, error) {
	var n int
	err := d.conn.QueryRow("SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

func (d *DB) targetProxySlots() int {
	v, ok, _ := d.GetSetting(SettingProxySlotCount)
	if !ok || v == "" {
		v, _, _ = d.GetSetting(SettingTargetAccountCount)
	}
	var n int
	fmt.Sscanf(v, "%d", &n)
	if n <= 0 {
		n = DefaultProxyIPCount
	}
	return n
}

var tagRe = regexp.MustCompile(`^warp-(\d+)$`)

func (d *DB) importExistingAccounts(projectRoot string) error {
	accountsDir := filepath.Join(projectRoot, "warp-accounts")
	entries, err := os.ReadDir(accountsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		m := tagRe.FindStringSubmatch(e.Name())
		if m == nil {
			continue
		}
		tag := e.Name()
		profilePath := filepath.Join(accountsDir, tag, "wgcf-profile.conf")
		prof, err := wgcf.ParseProfile(profilePath)
		if err != nil {
			continue
		}
		accountPath := filepath.Join(accountsDir, tag, "wgcf-account.toml")
		acct, _ := wgcf.ParseAccount(accountPath)
		if acct == nil {
			acct = &wgcf.Account{}
		}
		privateKey := prof.PrivateKey
		if privateKey == "" {
			privateKey = acct.PrivateKey
		}
		a := &AccountSeed{
			Tag:            tag,
			Directory:      "",
			PrivateKey:     privateKey,
			ClientID:       "",
			AccessToken:    acct.AccessToken,
			DeviceID:       acct.DeviceID,
			LicenseKey:     acct.LicenseKey,
			PeerPublicKey:  prof.PeerPublicKey,
			LocalAddressV4: prof.AddressV4,
			LocalAddressV6: prof.AddressV6,
			EndpointHost:   prof.EndpointHost,
			EndpointPort:   prof.EndpointPort,
			MTU:            prof.MTU,
			ListenPort:     0,
		}
		existing, _ := d.GetAccountByTag(tag)
		if existing != nil {
			if err := d.updateImportedAccount(a); err != nil {
				return err
			}
			continue
		}
		if err := d.seedInsertAccount(a); err != nil {
			return err
		}
	}
	return nil
}

type AccountSeed struct {
	Tag            string
	Directory      string
	PrivateKey     string
	ClientID       string
	AccessToken    string
	DeviceID       string
	LicenseKey     string
	PeerPublicKey  string
	LocalAddressV4 string
	LocalAddressV6 string
	EndpointHost   string
	EndpointPort   int
	MTU            int
	ListenPort     int
}

func (d *DB) seedInsertAccount(a *AccountSeed) error {
	_, err := d.conn.Exec(`INSERT INTO warp_accounts
		(tag, directory, status, private_key, client_id, access_token, device_id, license_key, peer_public_key, local_address_v4, local_address_v6,
		 endpoint_host, endpoint_port, mtu, listen_port, created_at)
		VALUES(?, ?, 'active', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.Tag, a.Directory, a.PrivateKey, a.ClientID, a.AccessToken, a.DeviceID, a.LicenseKey, a.PeerPublicKey, a.LocalAddressV4, a.LocalAddressV6,
		a.EndpointHost, a.EndpointPort, a.MTU, a.ListenPort, time.Now().UTC().Format(time.RFC3339))
	return err
}

func (d *DB) updateImportedAccount(a *AccountSeed) error {
	_, err := d.conn.Exec(`UPDATE warp_accounts SET
		directory = ?,
		private_key = ?,
		client_id = ?,
		access_token = ?,
		device_id = ?,
		license_key = ?,
		peer_public_key = ?,
		local_address_v4 = ?,
		local_address_v6 = ?,
		endpoint_host = ?,
		endpoint_port = ?,
		mtu = ?,
		listen_port = ?
		WHERE tag = ?`,
		a.Directory, a.PrivateKey, a.ClientID, a.AccessToken, a.DeviceID, a.LicenseKey, a.PeerPublicKey,
		a.LocalAddressV4, a.LocalAddressV6, a.EndpointHost, a.EndpointPort, a.MTU, a.ListenPort, a.Tag)
	return err
}
