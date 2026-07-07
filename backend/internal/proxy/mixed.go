package proxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// mixedServer is the single inbound proxy listener. It supports HTTP proxy
// requests, CONNECT, and SOCKS5 username/password auth on the same TCP port.
type mixedServer struct {
	resolve func(username, password string) []*Tunnel
	onUsage func(ProxyUsage)

	ln     net.Listener
	closed chan struct{}
	wg     sync.WaitGroup
}

type proxySession struct {
	username string
	tunnels  []*Tunnel
}

type ProxyUsage struct {
	ClientIP   string
	Username   string
	AccountTag string
	UpBytes    int64
	DownBytes  int64
}

func startProxy(bindAddr string, port int, resolve func(string, string) []*Tunnel, onUsage func(ProxyUsage)) (*mixedServer, error) {
	if bindAddr == "" {
		bindAddr = "0.0.0.0"
	}
	addr := fmt.Sprintf("%s:%d", bindAddr, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	s := &mixedServer{
		resolve: resolve,
		onUsage: onUsage,
		ln:      ln,
		closed:  make(chan struct{}),
	}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

func (s *mixedServer) port() int {
	if s.ln == nil {
		return 0
	}
	if a, ok := s.ln.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

func (s *mixedServer) acceptLoop() {
	defer s.wg.Done()
	for {
		c, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.closed:
				return
			default:
				time.Sleep(20 * time.Millisecond)
				continue
			}
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(c)
		}()
	}
}

func (s *mixedServer) Close() {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	_ = s.ln.Close()
	s.wg.Wait()
}

func (s *mixedServer) handle(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))
	clientIP := remoteIP(client.RemoteAddr())

	br := bufio.NewReader(client)
	first, err := br.Peek(1)
	if err != nil {
		return
	}

	if first[0] == 0x05 {
		s.handleSOCKS5(client, br, clientIP)
		return
	}
	s.handleHTTP(client, br, clientIP)
}

// ---------- SOCKS5 ----------

func (s *mixedServer) handleSOCKS5(client net.Conn, br *bufio.Reader, clientIP string) {
	ver, err := br.ReadByte()
	if err != nil || ver != 0x05 {
		return
	}
	nmethods, err := br.ReadByte()
	if err != nil {
		return
	}
	methods := make([]byte, nmethods)
	if _, err := io.ReadFull(br, methods); err != nil {
		return
	}

	if !bytesContains(methods, 0x02) {
		_, _ = client.Write([]byte{0x05, 0xff})
		return
	}
	if _, err := client.Write([]byte{0x05, 0x02}); err != nil {
		return
	}
	session := s.socks5Auth(client, br)
	if session == nil || len(session.tunnels) == 0 {
		return
	}

	hdr := make([]byte, 4)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return
	}
	if hdr[0] != 0x05 {
		return
	}
	cmd := hdr[1]
	atyp := hdr[3]

	var host string
	switch atyp {
	case 0x01:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(br, buf); err != nil {
			return
		}
		host = net.IP(buf).String()
	case 0x03:
		lb, err := br.ReadByte()
		if err != nil {
			return
		}
		buf := make([]byte, lb)
		if _, err := io.ReadFull(br, buf); err != nil {
			return
		}
		host = string(buf)
	case 0x04:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(br, buf); err != nil {
			return
		}
		host = net.IP(buf).String()
	default:
		s.socks5Reply(client, 0x08)
		return
	}

	portBuf := make([]byte, 2)
	if _, err := io.ReadFull(br, portBuf); err != nil {
		return
	}
	port := int(portBuf[0])<<8 | int(portBuf[1])

	if cmd != 0x01 {
		s.socks5Reply(client, 0x07)
		return
	}

	target := net.JoinHostPort(host, strconv.Itoa(port))
	remote, tun, err := s.dialVia(session.tunnels, target)
	if err != nil {
		s.socks5Reply(client, 0x05)
		return
	}
	defer remote.Close()

	if _, err := client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}

	_ = client.SetDeadline(time.Time{})
	s.relay(client, br, remote, tun, session, clientIP)
}

func (s *mixedServer) socks5Auth(client net.Conn, br *bufio.Reader) *proxySession {
	ver, err := br.ReadByte()
	if err != nil || ver != 0x01 {
		return nil
	}
	ulen, err := br.ReadByte()
	if err != nil {
		return nil
	}
	uname := make([]byte, ulen)
	if _, err := io.ReadFull(br, uname); err != nil {
		return nil
	}
	plen, err := br.ReadByte()
	if err != nil {
		return nil
	}
	passwd := make([]byte, plen)
	if _, err := io.ReadFull(br, passwd); err != nil {
		return nil
	}

	username := string(uname)
	tunnels := s.resolve(username, string(passwd))
	if len(tunnels) == 0 {
		_, _ = client.Write([]byte{0x01, 0x01})
		return nil
	}
	_, _ = client.Write([]byte{0x01, 0x00})
	return &proxySession{username: username, tunnels: tunnels}
}

func (s *mixedServer) socks5Reply(client net.Conn, code byte) {
	_, _ = client.Write([]byte{0x05, code, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
}

func bytesContains(items []byte, target byte) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

// ---------- HTTP ----------

func (s *mixedServer) handleHTTP(client net.Conn, br *bufio.Reader, clientIP string) {
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}

	session := s.httpAuthSession(req)
	if session == nil || len(session.tunnels) == 0 {
		resp := "HTTP/1.1 407 Proxy Authentication Required\r\n" +
			"Proxy-Authenticate: Basic realm=\"proxyforge\"\r\n" +
			"Content-Length: 0\r\n\r\n"
		_, _ = client.Write([]byte(resp))
		return
	}

	if req.Method == http.MethodConnect {
		s.httpConnect(client, br, req.Host, session, clientIP)
		return
	}
	s.httpForward(client, br, req, session, clientIP)
}

func (s *mixedServer) httpAuthSession(req *http.Request) *proxySession {
	auth := req.Header.Get("Proxy-Authorization")
	const prefix = "Basic "
	if !strings.HasPrefix(auth, prefix) {
		return nil
	}
	dec, err := base64.StdEncoding.DecodeString(auth[len(prefix):])
	if err != nil {
		return nil
	}
	user, pass, ok := strings.Cut(string(dec), ":")
	if !ok {
		return nil
	}
	tunnels := s.resolve(user, pass)
	if len(tunnels) == 0 {
		return nil
	}
	return &proxySession{username: user, tunnels: tunnels}
}

func (s *mixedServer) httpConnect(client net.Conn, br *bufio.Reader, hostport string, session *proxySession, clientIP string) {
	if !strings.Contains(hostport, ":") {
		hostport = hostport + ":443"
	}
	remote, tun, err := s.dialVia(session.tunnels, hostport)
	if err != nil {
		_, _ = client.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer remote.Close()

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	s.relay(client, br, remote, tun, session, clientIP)
}

func (s *mixedServer) httpForward(client net.Conn, br *bufio.Reader, req *http.Request, session *proxySession, clientIP string) {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	if !strings.Contains(host, ":") {
		host = host + ":80"
	}
	remote, tun, err := s.dialVia(session.tunnels, host)
	if err != nil {
		_, _ = client.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer remote.Close()

	req.Header.Del("Proxy-Authorization")
	req.Header.Del("Proxy-Connection")
	req.RequestURI = ""

	if err := req.Write(remote); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	s.relay(client, br, remote, tun, session, clientIP)
}

// ---------- shared ----------

func (s *mixedServer) dialVia(tunnels []*Tunnel, target string) (net.Conn, *Tunnel, error) {
	if len(tunnels) == 0 {
		return nil, nil, fmt.Errorf("no tunnel")
	}
	var lastErr error
	for _, tun := range tunnels {
		if tun == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		start := time.Now()
		conn, err := tun.DialContext(ctx, "tcp", target)
		cancel()
		tun.noteDial(time.Since(start), err)
		if err == nil {
			return conn, tun, nil
		}
		lastErr = err
		log.Printf("[proxy] dial %s via %s/%s failed: %v", target, tun.cfg.Tag, tun.transport, err)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no usable tunnel")
	}
	return nil, nil, lastErr
}

func (s *mixedServer) relay(client net.Conn, br *bufio.Reader, remote net.Conn, tun *Tunnel, session *proxySession, clientIP string) {
	done := make(chan struct{}, 2)
	var upBytes int64
	var downBytes int64

	go func() {
		n, _ := io.Copy(remote, br)
		tun.txBytes.Add(n)
		upBytes = n
		if tc, ok := remote.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	go func() {
		n, _ := io.Copy(client, remote)
		tun.rxBytes.Add(n)
		downBytes = n
		if tc, ok := client.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	<-done
	<-done

	if s.onUsage != nil && tun != nil && (upBytes > 0 || downBytes > 0) {
		username := ""
		if session != nil {
			username = session.username
		}
		s.onUsage(ProxyUsage{
			ClientIP:   clientIP,
			Username:   username,
			AccountTag: tun.cfg.Tag,
			UpBytes:    upBytes,
			DownBytes:  downBytes,
		})
	}
}

func remoteIP(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}
	return host
}
