package proxy

import "testing"

func TestDisplayUserAndFormatSize(t *testing.T) {
	if got := displayUser("auto"); got != "统一轮换" {
		t.Fatalf("auto => %q", got)
	}
	if got := displayUser("pf-001"); got != "pf-001" {
		t.Fatalf("pf-001 => %q", got)
	}
	if got := displayUser("node-abcdef012345"); got != "节点-abcdef01" {
		t.Fatalf("node => %q", got)
	}
	if got := displayEgress("node-xyz"); got != "Agent-xyz" {
		t.Fatalf("egress node => %q", got)
	}
	if got := formatDataSize(1536); got != "1.5 KB" {
		t.Fatalf("size => %q", got)
	}
}
