package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrojanUDPPacketFramingMatchesMihomoLimit(t *testing.T) {
	payload := bytes.Repeat([]byte{0x5a}, trojanUDPMaxFramePayload+37)
	var stream bytes.Buffer
	var mu sync.Mutex
	written, err := writeTrojanUDPPacket(&stream, &mu, "2001:db8::1", 3478, payload)
	if err != nil || written != len(payload) {
		t.Fatalf("write UDP frames = %d/%v", written, err)
	}

	first, err := readTrojanUDPPacket(&stream)
	if err != nil {
		t.Fatal(err)
	}
	second, err := readTrojanUDPPacket(&stream)
	if err != nil {
		t.Fatal(err)
	}
	joined := append(append([]byte(nil), first.payload...), second.payload...)
	if first.host != "2001:db8::1" || first.port != 3478 || len(first.payload) != trojanUDPMaxFramePayload || !bytes.Equal(joined, payload) {
		t.Fatal("Trojan UDP frame split did not round trip")
	}
}

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

type trojanUDPEgress struct {
	dialed chan string
	tx     atomic.Int64
	rx     atomic.Int64
}

func (e *trojanUDPEgress) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	e.dialed <- network + "/" + address
	return (&net.Dialer{}).DialContext(ctx, network, address)
}

func (e *trojanUDPEgress) Tag() string                   { return "warp-udp" }
func (e *trojanUDPEgress) Kind() string                  { return "test-warp" }
func (e *trojanUDPEgress) SupportsUDP() bool             { return true }
func (e *trojanUDPEgress) NoteDial(time.Duration, error) {}
func (e *trojanUDPEgress) AddTx(n int64)                 { e.tx.Add(n) }
func (e *trojanUDPEgress) AddRx(n int64)                 { e.rx.Add(n) }

func TestHandleTrojanUDPRelaysDatagram(t *testing.T) {
	echo, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	echoDone := make(chan struct{})
	go func() {
		defer close(echoDone)
		buffer := make([]byte, 1024)
		n, peer, err := echo.ReadFromUDP(buffer)
		if err == nil {
			_, _ = echo.WriteToUDP(append([]byte("pong:"), buffer[:n]...), peer)
		}
	}()

	credential := TrojanCredential("pf-udp", "secret")
	wantDigest := trojanAuthDigest(credential)
	egress := &trojanUDPEgress{dialed: make(chan string, 1)}
	usage := make(chan ProxyUsage, 1)
	s := &mixedServer{
		resolveTrojan: func(got [trojanAuthDigestSize]byte, _ string) (string, []Egress) {
			if got != wantDigest {
				return "", nil
			}
			return "pf-udp", []Egress{egress}
		},
		onUsage: func(got ProxyUsage) { usage <- got },
	}

	client, server := net.Pipe()
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		defer server.Close()
		s.handleTrojan(server, bufio.NewReader(server), "192.0.2.10")
	}()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))

	digestHex := make([]byte, hex.EncodedLen(len(wantDigest)))
	hex.Encode(digestHex, wantDigest[:])
	udpAddr := echo.LocalAddr().(*net.UDPAddr)
	request := append(digestHex, '\r', '\n', trojanUDPCommand)
	request, err = appendTrojanAddress(request, udpAddr.IP.String(), udpAddr.Port)
	if err != nil {
		t.Fatal(err)
	}
	request = append(request, '\r', '\n')
	request, err = appendTrojanAddress(request, udpAddr.IP.String(), udpAddr.Port)
	if err != nil {
		t.Fatal(err)
	}
	request = binary.BigEndian.AppendUint16(request, uint16(len("ping")))
	request = append(request, '\r', '\n')
	request = append(request, "ping"...)
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}

	packet, err := readTrojanUDPPacket(client)
	if err != nil {
		t.Fatal(err)
	}
	if string(packet.payload) != "pong:ping" || packet.port != udpAddr.Port {
		t.Fatalf("UDP response = %s:%d %q", packet.host, packet.port, packet.payload)
	}
	if got := <-egress.dialed; got != "udp/"+udpAddr.String() {
		t.Fatalf("egress dial = %q, want udp/%s", got, udpAddr)
	}

	_ = client.Close()
	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Trojan UDP handler did not stop")
	}
	<-echoDone

	select {
	case got := <-usage:
		if got.Username != "pf-udp" || got.AccountTag != "warp-udp" || got.UpBytes != 4 || got.DownBytes != 9 {
			t.Fatalf("UDP usage = %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("missing Trojan UDP usage")
	}
	if egress.tx.Load() != 4 || egress.rx.Load() != 9 {
		t.Fatalf("egress UDP counters = %d/%d", egress.tx.Load(), egress.rx.Load())
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
func (e *trojanEchoEgress) SupportsUDP() bool             { return true }
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
