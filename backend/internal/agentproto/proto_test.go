package agentproto

import (
	"bytes"
	"net"
	"strings"
	"testing"
	"time"
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

func TestRequestRoundTripAndLegacyFallback(t *testing.T) {
	for _, network := range []string{"tcp", "udp"} {
		var buf bytes.Buffer
		if err := WriteRequest(&buf, network, "1.2.3.4:443"); err != nil {
			t.Fatal(err)
		}
		gotNetwork, gotTarget, err := ReadRequest(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if gotNetwork != network || gotTarget != "1.2.3.4:443" {
			t.Fatalf("request = %s/%s, want %s/1.2.3.4:443", gotNetwork, gotTarget, network)
		}
	}

	var legacy bytes.Buffer
	if err := WriteTarget(&legacy, "example.com:80"); err != nil {
		t.Fatal(err)
	}
	network, target, err := ReadRequest(&legacy)
	if err != nil || network != "tcp" || target != "example.com:80" {
		t.Fatalf("legacy request = %s/%s/%v", network, target, err)
	}
}

func TestPacketConnPreservesDatagramBoundaries(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	_ = left.SetDeadline(time.Now().Add(time.Second))
	_ = right.SetDeadline(time.Now().Add(time.Second))

	a := NewPacketConn(left, nil)
	b := NewPacketConn(right, nil)
	done := make(chan error, 1)
	go func() {
		if _, err := a.Write([]byte("first")); err != nil {
			done <- err
			return
		}
		_, err := a.Write([]byte("second"))
		done <- err
	}()

	buf := make([]byte, 16)
	n, err := b.Read(buf)
	if err != nil || string(buf[:n]) != "first" {
		t.Fatalf("first packet = %q/%v", buf[:n], err)
	}
	n, err = b.Read(buf)
	if err != nil || string(buf[:n]) != "second" {
		t.Fatalf("second packet = %q/%v", buf[:n], err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	go func() {
		_, err := a.Write([]byte("oversized"))
		done <- err
	}()
	small := make([]byte, 4)
	n, err = b.Read(small)
	if err != nil || string(small[:n]) != "over" {
		t.Fatalf("truncated packet = %q/%v", small[:n], err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSupportsUDPVersion(t *testing.T) {
	if SupportsUDPVersion("1") || !SupportsUDPVersion("2") || !SupportsUDPVersion("3") || SupportsUDPVersion("bad") {
		t.Fatal("unexpected UDP protocol-version capability")
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
