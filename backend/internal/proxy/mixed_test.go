package proxy

import (
	"net"
	"strconv"
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
