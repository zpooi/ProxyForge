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
	// SettingAgentToken 是远程 agent 注册的准入 token；SettingPanelPublicHost
	// 是主控对外可访问的地址（agent 反连、下载安装脚本用），为空时回退到请求 Host。
	SettingAgentToken      = "agent_token"
	SettingPanelPublicHost = "panel_public_host"
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
