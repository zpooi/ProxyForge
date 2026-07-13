package handlers

import (
	"net/url"
	"strings"
	"testing"
)

func TestValidatedSettingsNormalizesAndMirrorsValues(t *testing.T) {
	got, err := validatedSettings(url.Values{
		SettingProxySlotCount:       {"25"},
		SettingProxyPort:            {" 7843 "},
		SettingDedupIntervalSeconds: {"300"},
		SettingAutoGeneration:       {"ON"},
		SettingProxyTLS:             {"off"},
		SettingWarpTransport:        {"wg"},
		SettingTunnelIPFamily:       {"v4"},
		SettingProxyDNSMode:         {"warp"},
		SettingProxyListenAddr:      {"127.0.0.1"},
		SettingProxyPublicHost:      {"Proxy.Example.COM"},
		SettingProxyPassword:        {"  p:# safe password  "},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		SettingProxySlotCount:       "25",
		SettingTargetAccountCount:   "25",
		SettingProxyPort:            "7843",
		SettingDedupIntervalSeconds: "300",
		SettingAutoGeneration:       "on",
		SettingProxyTLS:             "off",
		SettingWarpTransport:        "wireguard",
		SettingTunnelIPFamily:       "ipv4",
		SettingProxyDNSMode:         "tunnel",
		SettingProxyListenAddr:      "127.0.0.1",
		SettingProxyPublicHost:      "proxy.example.com",
		SettingProxyPassword:        "p:# safe password",
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("%s = %q, want %q", key, got[key], value)
		}
	}
}

func TestValidatedSettingsDoesNotClearOmittedSecrets(t *testing.T) {
	got, err := validatedSettings(url.Values{SettingProxyPort: {"9000"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[SettingProxyPassword]; ok {
		t.Fatal("omitted proxy password should not be overwritten")
	}
	if _, ok := got[SettingProxyPublicHost]; ok {
		t.Fatal("omitted public host should not be overwritten")
	}
}

func TestValidatedSettingsRejectsUnsafeOrUnboundedValues(t *testing.T) {
	tests := []url.Values{
		{SettingProxySlotCount: {"0"}},
		{SettingProxySlotCount: {"1000000"}},
		{SettingProxyPort: {"70000"}},
		{SettingDedupIntervalSeconds: {"5"}},
		{SettingProxyListenAddr: {"localhost"}},
		{SettingProxyPublicHost: {"https://proxy.example.com:7843/path"}},
		{SettingProxyPublicHost: {"proxy.example.com\nserver: evil"}},
		{SettingProxyPassword: {"hello\nworld"}},
		{SettingProxyPassword: {"too-short"}},
		{SettingProxyPassword: {""}},
		{SettingProxyTLS: {"maybe"}},
	}
	for _, form := range tests {
		if _, err := validatedSettings(form); err == nil {
			t.Errorf("expected validation error for %#v", form)
		}
	}
}

func TestNormalizePublicHostAcceptsDomainAndIP(t *testing.T) {
	for in, want := range map[string]string{
		"proxy.example.com": "proxy.example.com",
		"203.0.113.10":      "203.0.113.10",
		"[2001:db8::1]":     "2001:db8::1",
		"":                  "",
	} {
		got, err := normalizePublicHost(in)
		if err != nil || got != want {
			t.Errorf("normalizePublicHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := normalizePublicHost(strings.Repeat("a", 64) + ".example"); err == nil {
		t.Fatal("oversized DNS label should be rejected")
	}
}
