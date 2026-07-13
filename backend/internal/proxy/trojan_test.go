package proxy

import (
	"bufio"
	"context"
	"encoding/hex"
	"io"
	"net"
	"testing"
	"time"
)

func TestTrojanCredentialIsUniqueAndDomainSeparated(t *testing.T) {
	a := TrojanCredential("pf-001", "legacy-secret")
	b := TrojanCredential("pf-002", "legacy-secret")
	if a == "" || b == "" || a == b {
		t.Fatalf("derived credentials are not unique: %q %q", a, b)
	}
	if a == "legacy-secret" || b == "legacy-secret" {
		t.Fatal("Trojan credential reused the legacy proxy password")
	}
	if TrojanCredential("", "secret") != "" || TrojanCredential("pf-001", "") != "" {
		t.Fatal("empty input credentials must fail closed")
	}
}

func TestResolveTrojanSelectsExactAgent(t *testing.T) {
	m := newRotateManager("node-a", "node-b")
	digest := trojanAuthDigest(TrojanCredential("node-b", "secret"))
	username, egresses := m.resolveTrojan(digest, "192.0.2.1")
	if username != "node-b" || len(egresses) != 1 || egresses[0].Tag() != "node-b" {
		t.Fatalf("resolved Trojan agent = %q %#v, want node-b", username, egresses)
	}

	wrong := trojanAuthDigest("wrong")
	if username, egresses := m.resolveTrojan(wrong, "192.0.2.1"); username != "" || egresses != nil {
		t.Fatalf("wrong Trojan credential resolved to %q %#v", username, egresses)
	}
}

type trojanEchoEgress struct {
	target chan string
}

func (e *trojanEchoEgress) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	client, server := net.Pipe()
	e.target <- address
	go func() {
		defer server.Close()
		payload := make([]byte, 4)
		if _, err := io.ReadFull(server, payload); err == nil {
			_, _ = server.Write(payload)
		}
	}()
	return client, nil
}

func (e *trojanEchoEgress) Tag() string                   { return "node-test" }
func (e *trojanEchoEgress) Kind() string                  { return "test" }
func (e *trojanEchoEgress) NoteDial(time.Duration, error) {}
func (e *trojanEchoEgress) AddTx(int64)                   {}
func (e *trojanEchoEgress) AddRx(int64)                   {}

func TestHandleTrojanConnectRelaysPayload(t *testing.T) {
	credential := TrojanCredential("node-test", "secret")
	wantDigest := trojanAuthDigest(credential)
	egress := &trojanEchoEgress{target: make(chan string, 1)}
	s := &mixedServer{
		resolveTrojan: func(got [trojanAuthDigestSize]byte, _ string) (string, []Egress) {
			if got != wantDigest {
				return "", nil
			}
			return "node-test", []Egress{egress}
		},
	}

	client, server := net.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		s.handleTrojan(server, bufio.NewReader(server), "192.0.2.1")
	}()
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))

	digestHex := make([]byte, hex.EncodedLen(len(wantDigest)))
	hex.Encode(digestHex, wantDigest[:])
	host := "example.com"
	header := append(digestHex, '\r', '\n', trojanConnectCommand, 0x03, byte(len(host)))
	header = append(header, host...)
	header = append(header, 0x01, 0xbb, '\r', '\n') // port 443
	header = append(header, []byte("ping")...)
	if _, err := client.Write(header); err != nil {
		t.Fatal(err)
	}

	var echoed [4]byte
	if _, err := io.ReadFull(client, echoed[:]); err != nil {
		t.Fatal(err)
	}
	if string(echoed[:]) != "ping" {
		t.Fatalf("echoed payload = %q", echoed)
	}
	if target := <-egress.target; target != "example.com:443" {
		t.Fatalf("dial target = %q, want example.com:443", target)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Trojan session did not finish after target closed")
	}
}
