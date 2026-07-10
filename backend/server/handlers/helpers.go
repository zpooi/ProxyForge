package handlers

import "fmt"

const (
	SettingTargetAccountCount   = "target_account_count"
	SettingProxySlotCount       = "proxy_slot_count"
	SettingAutoGeneration       = "auto_generation"
	SettingProxyPassword        = "proxy_password"
	SettingProxyListenAddr      = "proxy_listen_addr"
	SettingProxyPort            = "proxy_port"
	SettingWarpTransport        = "warp_transport"
	SettingTunnelIPFamily       = "tunnel_ip_family"
	SettingProxyDNSMode         = "proxy_dns_mode"
	SettingDedupIntervalSeconds = "dedup_interval_seconds"
	SettingSubscriptionToken    = "subscription_token"
	SettingProxyPublicHost      = "proxy_public_host"
	SettingProxyTLS             = "proxy_tls"
)

func parseInt(s string, dst *int) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil {
		return 0, err
	}
	*dst = n
	return n, nil
}
