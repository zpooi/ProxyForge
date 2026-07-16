package proxy

import (
	"bufio"
	"context"
	"crypto/tls"
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
	resolve       func(username, password, clientIP string) []Egress
	resolveTrojan func(auth [trojanAuthDigestSize]byte, clientIP string) (string, []Egress)
	onUsage       func(ProxyUsage)

	tlsConfig *tls.Config

	ln        net.Listener
	closed    chan struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once

	connMu sync.Mutex
	conns  map[net.Conn]struct{}
}

type proxySession struct {
	username string
	egresses []Egress
}

const relayCopyBufferSize = 256 << 10

var relayCopyBufferPool = sync.Pool{New: func() any {
	return make([]byte, relayCopyBufferSize)
}}

type ProxyUsage struct {
	ClientIP   string
	Username   string
	AccountTag string
	UpBytes    int64
	DownBytes  int64
}

func startProxy(bindAddr string, port int, resolve func(string, string, string) []Egress, onUsage func(ProxyUsage), tlsConfig *tls.Config) (*mixedServer, error) {
	if bindAddr == "" {
		bindAddr = "0.0.0.0"
	}
	addr := fmt.Sprintf("%s:%d", bindAddr, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen %s: %w", addr, err)
	}
	s := &mixedServer{
		resolve:   resolve,
		onUsage:   onUsage,
		tlsConfig: tlsConfig,
		ln:        ln,
		closed:    make(chan struct{}),
		conns:     make(map[net.Conn]struct{}),
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
		if !s.trackConn(c) {
			_ = c.Close()
			return
		}
		s.wg.Add(1)
		go func(conn net.Conn) {
			defer s.wg.Done()
			defer s.untrackConn(conn)
			s.handle(conn)
		}(c)
	}
}

func (s *mixedServer) Close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.ln.Close()
		// 仅关闭 listener 不会影响已经 Accept 的连接。主动关闭所有存量连接，避免
		// 配置重载或优雅退出被 CONNECT / SSE / WebSocket 等长连接无限阻塞。
		s.connMu.Lock()
		for conn := range s.conns {
			_ = conn.Close()
		}
		s.connMu.Unlock()
		s.wg.Wait()
	})
}

func (s *mixedServer) trackConn(conn net.Conn) bool {
	s.connMu.Lock()
	defer s.connMu.Unlock()
	select {
	case <-s.closed:
		return false
	default:
		s.conns[conn] = struct{}{}
		return true
	}
}

func (s *mixedServer) untrackConn(conn net.Conn) {
	s.connMu.Lock()
	delete(s.conns, conn)
	s.connMu.Unlock()
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

	// TLS 握手记录以 0x16 开头，和 SOCKS5(0x05)、HTTP(ASCII 字母)都不冲突。
	// 客户端用 https 代理连接时，先在这一跳套一层 TLS，把明文的 CONNECT 主机名
	// 藏进加密流里，避开审查中间盒基于主机名的连接重置。解密后按同样的方式
	// 重新分发下层的 SOCKS5 / HTTP 代理协议。
	if first[0] == 0x16 && s.tlsConfig != nil {
		tlsConn := tls.Server(&peekedConn{Conn: client, br: br}, s.tlsConfig)
		_ = tlsConn.SetDeadline(time.Now().Add(30 * time.Second))
		if err := tlsConn.Handshake(); err != nil {
			return
		}
		_ = tlsConn.SetDeadline(time.Time{})
		inner := bufio.NewReader(tlsConn)
		if peek, err := inner.Peek(1); err == nil && peek[0] == 0x05 {
			s.handleSOCKS5(tlsConn, inner, clientIP)
			return
		}
		s.handleHTTP(tlsConn, inner, clientIP)
		return
	}

	if first[0] == 0x05 {
		s.handleSOCKS5(client, br, clientIP)
		return
	}
	s.handleHTTP(client, br, clientIP)
}

// peekedConn 把已经 Peek 出首字节的 bufio.Reader 还原成 net.Conn，交给
// tls.Server 从头读取握手记录。写入方向仍直接走底层连接。
type peekedConn struct {
	net.Conn
	br *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) {
	return c.br.Read(p)
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
	session := s.socks5Auth(client, br, clientIP)
	if session == nil || len(session.egresses) == 0 {
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
	remote, eg, err := s.dialVia(session.egresses, target)
	if err != nil {
		s.socks5Reply(client, 0x05)
		return
	}
	defer remote.Close()

	if _, err := client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}

	_ = client.SetDeadline(time.Time{})
	s.relay(client, br, remote, eg, session, clientIP)
}

func (s *mixedServer) socks5Auth(client net.Conn, br *bufio.Reader, clientIP string) *proxySession {
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
	egresses := s.resolve(username, string(passwd), clientIP)
	if len(egresses) == 0 {
		_, _ = client.Write([]byte{0x01, 0x01})
		return nil
	}
	_, _ = client.Write([]byte{0x01, 0x00})
	return &proxySession{username: username, egresses: egresses}
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

	session := s.httpAuthSession(req, clientIP)
	if session == nil || len(session.egresses) == 0 {
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

func (s *mixedServer) httpAuthSession(req *http.Request, clientIP string) *proxySession {
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
	egresses := s.resolve(user, pass, clientIP)
	if len(egresses) == 0 {
		return nil
	}
	return &proxySession{username: user, egresses: egresses}
}

func (s *mixedServer) httpConnect(client net.Conn, br *bufio.Reader, hostport string, session *proxySession, clientIP string) {
	if !strings.Contains(hostport, ":") {
		hostport = hostport + ":443"
	}
	remote, eg, err := s.dialVia(session.egresses, hostport)
	if err != nil {
		_, _ = client.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer remote.Close()

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	_ = client.SetDeadline(time.Time{})
	s.relay(client, br, remote, eg, session, clientIP)
}

func (s *mixedServer) httpForward(client net.Conn, br *bufio.Reader, req *http.Request, session *proxySession, clientIP string) {
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	if !strings.Contains(host, ":") {
		host = host + ":80"
	}
	remote, eg, err := s.dialVia(session.egresses, host)
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
	s.relay(client, br, remote, eg, session, clientIP)
}

// ---------- shared ----------

func (s *mixedServer) dialVia(egresses []Egress, target string) (net.Conn, Egress, error) {
	return s.dialViaNetworkWithPolicy(egresses, "tcp", target, maxProxyDialAttempts, proxyDialAttemptTTL, proxyDialTotalTTL)
}

func (s *mixedServer) dialViaWithPolicy(egresses []Egress, target string, maxAttempts int, attemptTTL, totalTTL time.Duration) (net.Conn, Egress, error) {
	return s.dialViaNetworkWithPolicy(egresses, "tcp", target, maxAttempts, attemptTTL, totalTTL)
}

func (s *mixedServer) dialUDPVia(egresses []Egress, target string) (net.Conn, Egress, error) {
	return s.dialViaNetworkWithPolicy(egresses, "udp", target, maxProxyDialAttempts, proxyDialAttemptTTL, proxyDialTotalTTL)
}

func (s *mixedServer) dialViaNetworkWithPolicy(egresses []Egress, network, target string, maxAttempts int, attemptTTL, totalTTL time.Duration) (net.Conn, Egress, error) {
	if len(egresses) == 0 {
		return nil, nil, fmt.Errorf("no egress")
	}
	if network != "tcp" && network != "udp" {
		return nil, nil, fmt.Errorf("unsupported proxy network %q", network)
	}
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	overallCtx, overallCancel := context.WithTimeout(context.Background(), totalTTL)
	defer overallCancel()

	var lastErr error
	attempts := 0
	for _, eg := range egresses {
		if attempts >= maxAttempts || overallCtx.Err() != nil {
			break
		}
		if eg == nil {
			continue
		}
		if network == "udp" && !eg.SupportsUDP() {
			continue
		}
		attempts++
		ctx, cancel := context.WithTimeout(overallCtx, attemptTTL)
		start := time.Now()
		conn, err := eg.DialContext(ctx, network, target)
		cancel()
		eg.NoteDial(time.Since(start), err)
		if err == nil {
			return conn, eg, nil
		}
		lastErr = err
		log.Printf("[proxy] dial %s/%s via %s/%s failed: %v", network, target, eg.Tag(), eg.Kind(), err)
		if isPermanentTargetDialError(err) {
			break
		}
	}
	if lastErr == nil {
		if err := overallCtx.Err(); err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("no usable %s egress", network)
		}
	}
	return nil, nil, lastErr
}

func (s *mixedServer) relay(client net.Conn, br *bufio.Reader, remote net.Conn, eg Egress, session *proxySession, clientIP string) {
	done := make(chan struct{}, 2)
	var upBytes int64
	var downBytes int64

	go func() {
		n, _ := copyRelay(remote, br)
		eg.AddTx(n)
		upBytes = n
		if tc, ok := remote.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	go func() {
		downstream, flushDownstream := relayDownstreamWriter(client)
		n, _ := copyRelay(downstream, remote)
		_ = flushDownstream()
		eg.AddRx(n)
		downBytes = n
		if tc, ok := client.(interface{ CloseWrite() error }); ok {
			_ = tc.CloseWrite()
		}
		done <- struct{}{}
	}()

	<-done
	<-done

	if s.onUsage != nil && eg != nil && (upBytes > 0 || downBytes > 0) {
		username := ""
		if session != nil {
			username = session.username
		}
		s.onUsage(ProxyUsage{
			ClientIP:   clientIP,
			Username:   username,
			AccountTag: eg.Tag(),
			UpBytes:    upBytes,
			DownBytes:  downBytes,
		})
	}
}

// copyRelay uses substantially larger chunks than io.Copy's default 32 KiB.
// This matters for Trojan over WebSocket because every Write becomes a WS
// message; larger chunks reduce framing, locking, and nginx forwarding overhead
// during sustained downloads without changing stream semantics.
func copyRelay(dst io.Writer, src io.Reader) (int64, error) {
	buf := relayCopyBufferPool.Get().([]byte)
	defer relayCopyBufferPool.Put(buf)
	return io.CopyBuffer(dst, src, buf)
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
