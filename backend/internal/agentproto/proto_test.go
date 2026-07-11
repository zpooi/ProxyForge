package agentproto

import (
	"bytes"
	"strings"
	"testing"
)

func TestTargetRoundTrip(t *testing.T) {
	for _, target := range []string{"example.com:443", "1.2.3.4:80", "[2606:4700::1]:443"} {
		var buf bytes.Buffer
		if err := WriteTarget(&buf, target); err != nil {
			t.Fatalf("WriteTarget(%q): %v", target, err)
		}
		got, err := ReadTarget(&buf)
		if err != nil {
			t.Fatalf("ReadTarget(%q): %v", target, err)
		}
		if got != target {
			t.Fatalf("round trip: got %q, want %q", got, target)
		}
	}
}

func TestWriteTargetRejectsInvalidLength(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteTarget(&buf, ""); err == nil {
		t.Fatal("expected error for empty target")
	}
	if err := WriteTarget(&buf, strings.Repeat("a", maxTargetLen+1)); err == nil {
		t.Fatal("expected error for oversized target")
	}
}

func TestStatusRoundTrip(t *testing.T) {
	for _, ok := range []bool{true, false} {
		var buf bytes.Buffer
		if err := WriteStatus(&buf, ok); err != nil {
			t.Fatalf("WriteStatus(%v): %v", ok, err)
		}
		got, err := ReadStatus(&buf)
		if err != nil {
			t.Fatalf("ReadStatus(%v): %v", ok, err)
		}
		if got != ok {
			t.Fatalf("status round trip: got %v, want %v", got, ok)
		}
	}
}
