package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (h *Handlers) SettingsPage(w http.ResponseWriter, r *http.Request) {
	h.AppPage(w, r)
}

func (h *Handlers) SettingsJSON(w http.ResponseWriter, r *http.Request) {
	settings, err := h.DB.AllSettings()
	if err != nil {
		http.Error(w, "load settings failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"settings": settings})
}

func (h *Handlers) SettingsSave(w http.ResponseWriter, r *http.Request) {
	if err := h.saveSettings(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	http.Redirect(w, r, "/settings?saved=1", http.StatusFound)
}

func (h *Handlers) SettingsSaveJSON(w http.ResponseWriter, r *http.Request) {
	if err := h.saveSettings(r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	settings, err := h.DB.AllSettings()
	if err != nil {
		http.Error(w, "load settings failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "settings": settings})
}

func (h *Handlers) saveSettings(r *http.Request) error {
	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("bad request")
	}
	values, err := validatedSettings(r.PostForm)
	if err != nil {
		return err
	}
	if err := h.DB.SetSettings(values); err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	if len(values) > 0 {
		if err := h.Scheduler.Reconcile(); err != nil {
			// 设置已原子落盘。隧道启动可能因临时网络问题失败，后台健康检查仍会重试；
			// 不把一次瞬时失败伪装成“设置未保存”。
			log.Printf("[settings] saved but reconcile reported: %v", err)
		}
	}
	return nil
}

func validatedSettings(form url.Values) (map[string]string, error) {
	out := make(map[string]string)
	value := func(key string) (string, bool) {
		items, ok := form[key]
		if !ok || len(items) == 0 {
			return "", false
		}
		return items[len(items)-1], true
	}

	if raw, ok := value(SettingProxySlotCount); ok {
		n, err := boundedSettingInt("代理 IP 数", raw, 1, 500)
		if err != nil {
			return nil, err
		}
		v := strconv.Itoa(n)
		out[SettingProxySlotCount] = v
		out[SettingTargetAccountCount] = v
	} else if raw, ok := value(SettingTargetAccountCount); ok {
		// 兼容旧版表单；内部始终把两个历史键对齐。
		n, err := boundedSettingInt("代理 IP 数", raw, 1, 500)
		if err != nil {
			return nil, err
		}
		v := strconv.Itoa(n)
		out[SettingProxySlotCount] = v
		out[SettingTargetAccountCount] = v
	}

	if raw, ok := value(SettingProxyPort); ok {
		n, err := boundedSettingInt("代理端口", raw, 1, 65535)
		if err != nil {
			return nil, err
		}
		out[SettingProxyPort] = strconv.Itoa(n)
	}
	if raw, ok := value(SettingDedupIntervalSeconds); ok {
		n, err := boundedSettingInt("检测间隔", raw, 60, 86400)
		if err != nil {
			return nil, err
		}
		out[SettingDedupIntervalSeconds] = strconv.Itoa(n)
	}

	for _, item := range []struct {
		key     string
		label   string
		allowed map[string]string
	}{
		{SettingAutoGeneration, "自动生成 WARP", map[string]string{"on": "on", "off": "off"}},
		{SettingProxyTLS, "代理 TLS", map[string]string{"on": "on", "off": "off"}},
		{SettingWarpTransport, "WARP 传输", map[string]string{"auto": "auto", "masque": "masque", "wireguard": "wireguard", "wg": "wireguard"}},
		{SettingTunnelIPFamily, "隧道 IP 类型", map[string]string{"ipv4": "ipv4", "v4": "ipv4", "ipv6": "ipv6", "v6": "ipv6", "dual": "dual"}},
		{SettingProxyDNSMode, "代理 DNS 模式", map[string]string{"system": "system", "tunnel": "tunnel", "warp": "tunnel"}},
	} {
		if raw, ok := value(item.key); ok {
			v := strings.ToLower(strings.TrimSpace(raw))
			canonical, valid := item.allowed[v]
			if !valid {
				return nil, fmt.Errorf("%s取值无效", item.label)
			}
			out[item.key] = canonical
		}
	}

	if raw, ok := value(SettingProxyListenAddr); ok {
		v := strings.TrimSpace(raw)
		ip := net.ParseIP(v)
		if ip == nil || ip.To4() == nil {
			return nil, fmt.Errorf("监听地址必须是 IPv4 地址")
		}
		out[SettingProxyListenAddr] = ip.String()
	}
	if raw, ok := value(SettingProxyPublicHost); ok {
		v, err := normalizePublicHost(raw)
		if err != nil {
			return nil, err
		}
		out[SettingProxyPublicHost] = v
	}
	if raw, ok := value(SettingProxyPassword); ok {
		v := strings.TrimSpace(raw)
		if len(v) > 256 || strings.ContainsAny(v, "\x00\r\n") {
			return nil, fmt.Errorf("全局代理密码不能超过 256 字符或包含换行")
		}
		out[SettingProxyPassword] = v
	}
	return out, nil
}

func boundedSettingInt(label, raw string, min, max int) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s必须在 %d 到 %d 之间", label, min, max)
	}
	return n, nil
}

func normalizePublicHost(raw string) (string, error) {
	host := strings.TrimSpace(raw)
	if host == "" {
		return "", nil
	}
	if len(host) > 253 || strings.ContainsAny(host, "\x00\r\n\t /?#@,") || strings.Contains(host, "://") {
		return "", fmt.Errorf("代理对外地址只能填写域名或 IP，不能包含协议、端口或路径")
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	if strings.Contains(host, ":") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return "", fmt.Errorf("代理对外地址格式无效")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("代理对外地址格式无效")
		}
		for _, r := range label {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-') {
				return "", fmt.Errorf("代理对外地址格式无效")
			}
		}
	}
	return strings.ToLower(host), nil
}
