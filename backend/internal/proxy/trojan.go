package proxy

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

const (
	trojanAuthDigestSize = sha256.Size224
	trojanAuthLineSize   = trojanAuthDigestSize*2 + 2
	trojanConnectCommand = 0x01
	trojanUDPCommand     = 0x03
)

// TrojanCredential derives a protocol-specific credential from an existing
// proxy username/password pair. Domain separation prevents a leaked Clash
// credential from being reused directly against the legacy HTTP/SOCKS5 port,
// while including the username gives agent nodes unique credentials even when
// they share the same global proxy password.
func TrojanCredential(username, password string) string {
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if username == "" || password == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(password))
	_, _ = mac.Write([]byte("proxyforge/trojan/v1\x00"))
	_, _ = mac.Write([]byte(username))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func trojanAuthDigest(credential string) [trojanAuthDigestSize]byte {
	return sha256.Sum224([]byte(credential))
}

func trojanAuthMatches(got [trojanAuthDigestSize]byte, username, password string) bool {
	credential := TrojanCredential(username, password)
	if credential == "" {
		return false
	}
	want := trojanAuthDigest(credential)
	return subtle.ConstantTimeCompare(want[:], got[:]) == 1
}

// resolveTrojan maps Trojan's password-only authentication back to the
// username-based routing model used by HTTP/SOCKS5. All candidates are checked
// before returning so a remote timing sample does not reveal a credential's
// position in the slot list.
func (m *Manager) resolveTrojan(auth [trojanAuthDigestSize]byte, clientIP string) (string, []Egress) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var matchedUsername string
	var matchedEgresses []Egress
	for username, slot := range m.slots {
		if trojanAuthMatches(auth, username, slot.Password) {
			matchedUsername = username
			matchedEgresses = tunnelsAsEgress(m.slotCandidatesLocked(slot.AccountTag))
		}
	}

	// Remote agents share the global proxy password on the legacy listener.
	// The username-dependent derivation above still gives every Trojan node a
	// different password and lets us select its exact remote egress here.
	if m.agentResolver != nil {
		for _, eg := range m.agentResolver.OnlineEgresses() {
			if eg == nil || !strings.HasPrefix(eg.Tag(), NodeUsernamePrefix) {
				continue
			}
			if trojanAuthMatches(auth, eg.Tag(), m.password) {
				matchedUsername = eg.Tag()
				matchedEgresses = []Egress{eg}
			}
		}
	}
	return matchedUsername, matchedEgresses
}

// ServeTrojan serves one already-encrypted Trojan byte stream. The caller is
// responsible for TLS/WebSocket termination and for closing conn.
func (m *Manager) ServeTrojan(conn net.Conn, clientIP string) {
	if m == nil || conn == nil {
		return
	}
	s := &mixedServer{
		resolveTrojan: m.resolveTrojan,
		onUsage:       m.recordUsage,
	}
	s.handleTrojan(conn, bufio.NewReader(conn), clientIP)
}

func (s *mixedServer) handleTrojan(client net.Conn, br *bufio.Reader, clientIP string) {
	_ = client.SetDeadline(time.Now().Add(30 * time.Second))
	auth, ok := readTrojanAuth(br)
	if !ok || s.resolveTrojan == nil {
		return
	}
	username, egresses := s.resolveTrojan(auth, clientIP)
	if username == "" || len(egresses) == 0 {
		return
	}

	command, err := br.ReadByte()
	if err != nil {
		return
	}
	host, port, ok := readTrojanTarget(br)
	if !ok {
		return
	}
	var terminator [2]byte
	if _, err := io.ReadFull(br, terminator[:]); err != nil || terminator != [2]byte{'\r', '\n'} {
		return
	}
	if command == trojanUDPCommand {
		s.relayTrojanUDP(client, br, egresses, &proxySession{username: username, egresses: egresses}, clientIP)
		return
	}
	if command != trojanConnectCommand {
		return
	}

	remote, eg, err := s.dialVia(egresses, net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return
	}
	defer remote.Close()

	_ = client.SetDeadline(time.Time{})
	s.relay(client, br, remote, eg, &proxySession{username: username, egresses: egresses}, clientIP)
}

func readTrojanAuth(br *bufio.Reader) ([trojanAuthDigestSize]byte, bool) {
	var digest [trojanAuthDigestSize]byte
	var line [trojanAuthLineSize]byte
	if _, err := io.ReadFull(br, line[:]); err != nil {
		return digest, false
	}
	if line[len(line)-2] != '\r' || line[len(line)-1] != '\n' {
		return digest, false
	}
	n, err := hex.Decode(digest[:], line[:len(line)-2])
	return digest, err == nil && n == len(digest)
}

func readTrojanTarget(br *bufio.Reader) (string, int, bool) {
	return readTrojanAddress(br)
}
