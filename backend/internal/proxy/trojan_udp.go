package proxy

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// Mihomo's Trojan implementation accepts at most 8192 bytes in one
	// framed packet. Normal Internet UDP packets are far smaller than this.
	trojanUDPMaxFramePayload = 8192
	trojanUDPReadBufferSize  = 65535
	trojanUDPMaxAssociations = 64
	trojanUDPIdleTimeout     = 2 * time.Minute
	trojanUDPClientWriteTTL  = 30 * time.Second
)

type trojanUDPPacket struct {
	host    string
	port    int
	payload []byte
}

// readTrojanAddress reads the SOCKS5-like address used by both the initial
// Trojan request and every UDP packet frame.
func readTrojanAddress(r io.Reader) (string, int, bool) {
	var first [1]byte
	if _, err := io.ReadFull(r, first[:]); err != nil {
		return "", 0, false
	}

	var host string
	switch first[0] {
	case 0x01:
		var raw [net.IPv4len]byte
		if _, err := io.ReadFull(r, raw[:]); err != nil {
			return "", 0, false
		}
		host = net.IP(raw[:]).String()
	case 0x03:
		if _, err := io.ReadFull(r, first[:]); err != nil || first[0] == 0 {
			return "", 0, false
		}
		raw := make([]byte, int(first[0]))
		if _, err := io.ReadFull(r, raw); err != nil {
			return "", 0, false
		}
		host = string(raw)
	case 0x04:
		var raw [net.IPv6len]byte
		if _, err := io.ReadFull(r, raw[:]); err != nil {
			return "", 0, false
		}
		host = net.IP(raw[:]).String()
	default:
		return "", 0, false
	}

	var rawPort [2]byte
	if _, err := io.ReadFull(r, rawPort[:]); err != nil {
		return "", 0, false
	}
	port := int(binary.BigEndian.Uint16(rawPort[:]))
	return host, port, host != "" && port > 0
}

func readTrojanUDPPacket(r io.Reader) (trojanUDPPacket, error) {
	return readTrojanUDPPacketInto(r, make([]byte, trojanUDPMaxFramePayload))
}

func readTrojanUDPPacketInto(r io.Reader, payloadBuffer []byte) (trojanUDPPacket, error) {
	host, port, ok := readTrojanAddress(r)
	if !ok {
		return trojanUDPPacket{}, fmt.Errorf("invalid Trojan UDP destination")
	}

	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return trojanUDPPacket{}, err
	}
	length := int(binary.BigEndian.Uint16(header[:2]))
	if length > trojanUDPMaxFramePayload {
		return trojanUDPPacket{}, fmt.Errorf("Trojan UDP payload is too large: %d", length)
	}
	if length > len(payloadBuffer) {
		return trojanUDPPacket{}, io.ErrShortBuffer
	}
	if header[2] != '\r' || header[3] != '\n' {
		return trojanUDPPacket{}, fmt.Errorf("invalid Trojan UDP frame terminator")
	}
	payload := payloadBuffer[:length]
	if _, err := io.ReadFull(r, payload); err != nil {
		return trojanUDPPacket{}, err
	}
	return trojanUDPPacket{host: host, port: port, payload: payload}, nil
}

func appendTrojanAddress(dst []byte, host string, port int) ([]byte, error) {
	if port <= 0 || port > 65535 {
		return nil, fmt.Errorf("invalid Trojan UDP port %d", port)
	}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			dst = append(dst, 0x01)
			dst = append(dst, v4...)
		} else {
			dst = append(dst, 0x04)
			dst = append(dst, ip.To16()...)
		}
	} else {
		if len(host) == 0 || len(host) > 255 {
			return nil, fmt.Errorf("invalid Trojan UDP host length %d", len(host))
		}
		dst = append(dst, 0x03, byte(len(host)))
		dst = append(dst, host...)
	}
	return binary.BigEndian.AppendUint16(dst, uint16(port)), nil
}

func writeTrojanUDPPacket(w io.Writer, mu *sync.Mutex, host string, port int, payload []byte) (int, error) {
	written := 0
	for {
		chunk := payload
		if len(chunk) > trojanUDPMaxFramePayload {
			chunk = chunk[:trojanUDPMaxFramePayload]
		}

		frame, err := appendTrojanAddress(make([]byte, 0, len(chunk)+32), host, port)
		if err != nil {
			return written, err
		}
		frame = binary.BigEndian.AppendUint16(frame, uint16(len(chunk)))
		frame = append(frame, '\r', '\n')
		frame = append(frame, chunk...)

		mu.Lock()
		deadlineConn, hasDeadline := w.(interface{ SetWriteDeadline(time.Time) error })
		if hasDeadline {
			_ = deadlineConn.SetWriteDeadline(time.Now().Add(trojanUDPClientWriteTTL))
		}
		var frameBytes int64
		frameBytes, err = io.Copy(w, bytes.NewReader(frame))
		if hasDeadline {
			_ = deadlineConn.SetWriteDeadline(time.Time{})
		}
		mu.Unlock()
		if err != nil {
			return written, err
		}
		if frameBytes != int64(len(frame)) {
			return written, io.ErrShortWrite
		}
		written += len(chunk)
		if len(payload) <= len(chunk) {
			return written, nil
		}
		payload = payload[len(chunk):]
	}
}

type trojanUDPAssociation struct {
	conn   net.Conn
	egress Egress
	target string
	host   string
	port   int
	relay  *trojanUDPRelay

	closed  atomic.Bool
	lastUse atomic.Int64
	upBytes atomic.Int64
	dnBytes atomic.Int64
	useMu   sync.RWMutex
	once    sync.Once
}

func (a *trojanUDPAssociation) touch() {
	a.lastUse.Store(time.Now().UnixNano())
}

func (a *trojanUDPAssociation) close() {
	a.once.Do(func() {
		a.useMu.Lock()
		defer a.useMu.Unlock()
		a.closed.Store(true)
		_ = a.conn.Close()
		a.relay.reportUsage(a)
	})
}

type trojanUDPRelay struct {
	server   *mixedServer
	client   net.Conn
	session  *proxySession
	clientIP string

	writeMu sync.Mutex
	abort   sync.Once
	wg      sync.WaitGroup
	active  map[string]*trojanUDPAssociation
}

func (s *mixedServer) relayTrojanUDP(client net.Conn, br io.Reader, egresses []Egress, session *proxySession, clientIP string) {
	relay := &trojanUDPRelay{
		server:   s,
		client:   client,
		session:  session,
		clientIP: clientIP,
		active:   make(map[string]*trojanUDPAssociation),
	}
	defer relay.close()

	_ = client.SetDeadline(time.Time{})
	payloadBuffer := make([]byte, trojanUDPMaxFramePayload)
	for {
		packet, err := readTrojanUDPPacketInto(br, payloadBuffer)
		if err != nil {
			return
		}
		target := net.JoinHostPort(packet.host, strconv.Itoa(packet.port))
		if err := relay.send(egresses, target, packet); err != nil {
			// A single unreachable UDP target must not tear down unrelated DNS,
			// STUN, or QUIC flows sharing this Trojan association.
			continue
		}
	}
}

func (r *trojanUDPRelay) send(egresses []Egress, target string, packet trojanUDPPacket) error {
	for attempt := 0; attempt < 2; attempt++ {
		association, err := r.association(egresses, target, packet.host, packet.port)
		if err != nil {
			return err
		}
		association.useMu.RLock()
		association.touch()
		n, err := association.conn.Write(packet.payload)
		if err == nil && n == len(packet.payload) {
			association.upBytes.Add(int64(n))
			association.egress.AddTx(int64(n))
			association.useMu.RUnlock()
			return nil
		}
		association.useMu.RUnlock()
		association.close()
		delete(r.active, target)
		if err == nil {
			err = io.ErrShortWrite
		}
		if attempt == 1 {
			return err
		}
	}
	return net.ErrClosed
}

func (r *trojanUDPRelay) association(egresses []Egress, target, host string, port int) (*trojanUDPAssociation, error) {
	if association := r.active[target]; association != nil {
		if !association.closed.Load() {
			return association, nil
		}
		delete(r.active, target)
	}
	for key, association := range r.active {
		if association.closed.Load() {
			delete(r.active, key)
		}
	}
	if len(r.active) >= trojanUDPMaxAssociations {
		return nil, fmt.Errorf("too many Trojan UDP destinations")
	}

	conn, egress, err := r.server.dialUDPVia(egresses, target)
	if err != nil {
		return nil, err
	}
	association := &trojanUDPAssociation{
		conn:   conn,
		egress: egress,
		target: target,
		host:   host,
		port:   port,
		relay:  r,
	}
	association.touch()
	r.active[target] = association
	r.wg.Add(1)
	go r.readResponses(association)
	return association, nil
}

func (r *trojanUDPRelay) readResponses(association *trojanUDPAssociation) {
	defer r.wg.Done()
	defer association.close()

	buffer := make([]byte, trojanUDPReadBufferSize)
	for {
		lastUse := time.Unix(0, association.lastUse.Load())
		_ = association.conn.SetReadDeadline(lastUse.Add(trojanUDPIdleTimeout))
		n, err := association.conn.Read(buffer)
		if err != nil {
			latestUse := time.Unix(0, association.lastUse.Load())
			if ne, ok := err.(net.Error); ok && ne.Timeout() && time.Since(latestUse) < trojanUDPIdleTimeout {
				continue
			}
			return
		}
		association.touch()
		association.useMu.RLock()
		if association.closed.Load() {
			association.useMu.RUnlock()
			return
		}
		host, port := udpResponseAddress(association)
		written, err := writeTrojanUDPPacket(r.client, &r.writeMu, host, port, buffer[:n])
		if err != nil {
			association.useMu.RUnlock()
			r.abort.Do(func() { _ = r.client.Close() })
			return
		}
		association.dnBytes.Add(int64(written))
		association.egress.AddRx(int64(written))
		association.useMu.RUnlock()
	}
}

func udpResponseAddress(association *trojanUDPAssociation) (string, int) {
	if association != nil && association.conn != nil {
		switch addr := association.conn.RemoteAddr().(type) {
		case *net.UDPAddr:
			if addr.IP != nil && addr.Port > 0 {
				return addr.IP.String(), addr.Port
			}
		case *net.TCPAddr:
			if addr.IP != nil && addr.Port > 0 {
				return addr.IP.String(), addr.Port
			}
		}
	}
	return association.host, association.port
}

func (r *trojanUDPRelay) close() {
	r.abort.Do(func() { _ = r.client.Close() })
	for _, association := range r.active {
		association.close()
	}
	r.wg.Wait()
}

func (r *trojanUDPRelay) reportUsage(association *trojanUDPAssociation) {
	if r.server.onUsage == nil {
		return
	}
	up := association.upBytes.Load()
	down := association.dnBytes.Load()
	if up == 0 && down == 0 {
		return
	}
	username := ""
	if r.session != nil {
		username = r.session.username
	}
	r.server.onUsage(ProxyUsage{
		ClientIP:   r.clientIP,
		Username:   username,
		AccountTag: association.egress.Tag(),
		UpBytes:    up,
		DownBytes:  down,
	})
}
