package proxy

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestMixedServerCloseTerminatesAcceptedConnections(t *testing.T) {
	srv, err := startProxy("127.0.0.1", 0, func(string, string, string) []Egress {
		return nil
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.port())))
	if err != nil {
		srv.Close()
		t.Fatal(err)
	}
	defer conn.Close()

	// 等待 acceptLoop 登记连接；连接不发送首字节，会一直阻塞在协议探测处。
	deadline := time.Now().Add(time.Second)
	for {
		srv.connMu.Lock()
		tracked := len(srv.conns)
		srv.connMu.Unlock()
		if tracked == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("accepted connection was not tracked")
		}
		time.Sleep(time.Millisecond)
	}

	done := make(chan struct{})
	go func() {
		srv.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("server Close blocked on an accepted idle connection")
	}

	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("client connection remained open after server Close")
	}

	// Close 必须幂等，方便 Manager.Stop 与配置重载安全重复调用。
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			srv.Close()
		}()
	}
	wg.Wait()
}

func TestTLSCapableListenerAcceptsPlaintextHTTP(t *testing.T) {
	tlsConfig, err := newSelfSignedTLSConfig("proxy.example.test")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := startProxy("127.0.0.1", 0, func(string, string, string) []Egress { return nil }, nil, tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.port())))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, _ = conn.Write([]byte("GET http://example.test/ HTTP/1.1\r\nHost: example.test\r\n\r\n"))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "407") {
		t.Fatalf("plaintext proxy response = %q, want 407", line)
	}
}

func TestTLSCapableListenerAcceptsPlaintextSOCKS5(t *testing.T) {
	tlsConfig, err := newSelfSignedTLSConfig("proxy.example.test")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := startProxy("127.0.0.1", 0, func(string, string, string) []Egress { return nil }, nil, tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.port())))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[0] != 0x05 || reply[1] != 0x02 {
		t.Fatalf("SOCKS5 method reply = %v, want username/password", reply)
	}
}

func TestTLSCapableListenerAcceptsTLS(t *testing.T) {
	tlsConfig, err := newSelfSignedTLSConfig("proxy.example.test")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := startProxy("127.0.0.1", 0, func(string, string, string) []Egress { return nil }, nil, tlsConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	conn, err := tls.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(srv.port())), &tls.Config{
		MinVersion:         tls.VersionTLS12,
		ServerName:         "proxy.example.test",
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	_, _ = conn.Write([]byte("GET http://example.test/ HTTP/1.1\r\nHost: example.test\r\n\r\n"))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "407") {
		t.Fatalf("TLS proxy response = %q, want 407", line)
	}
}

func TestRequiredPasswordMatchesFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		want, got string
		match     bool
	}{
		{want: "", got: "anything", match: false},
		{want: "secret", got: "", match: false},
		{want: "secret", got: "wrong", match: false},
		{want: "secret", got: "secret", match: true},
	} {
		if got := requiredPasswordMatches(tc.want, tc.got); got != tc.match {
			t.Errorf("requiredPasswordMatches(%q, %q) = %v", tc.want, tc.got, got)
		}
	}
}
