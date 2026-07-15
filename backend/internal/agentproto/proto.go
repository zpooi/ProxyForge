// Package agentproto 定义主控与远程 agent 之间在 yamux 流上的最小握手协议。
//
// 它刻意只依赖标准库，供主控（agenthub）和轻量 agent（cmd/pfagent）共享，
// 避免协议层反向依赖具体的 WARP 隧道实现。
package agentproto

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
)

func SupportsUDPVersion(raw string) bool {
	version, err := strconv.Atoi(strings.TrimSpace(raw))
	return err == nil && version >= 2
}

const (
	// ProtocolVersion is advertised during agent enrollment. Version 2 adds
	// network-aware dial requests and framed UDP while still accepting v1 TCP.
	ProtocolVersion = 2

	// 主控在每条 yamux 流开头写入目标地址，agent 本地拨号后回写 1 字节状态。
	// 状态字节让 DialContext 能感知远端拨号失败，从而在客户端层触发故障转移。
	DialOK   = 0x00
	DialFail = 0x01

	// 目标地址（host:port）长度上限。域名最长 253，加端口约 260，留足余量。
	maxTargetLen = 512

	dialRequestMagic0 = 0x50
	dialRequestMagic1 = 0x46
	dialNetworkTCP    = 0x01
	dialNetworkUDP    = 0x03
)

// WriteRequest adds a distinguishable network prefix in protocol v2. A v2
// agent still accepts the old length-prefixed TCP request, which makes a
// controller-first rolling upgrade safe.
func WriteRequest(w io.Writer, network, target string) error {
	var code byte
	switch network {
	case "tcp":
		code = dialNetworkTCP
	case "udp":
		code = dialNetworkUDP
	default:
		return fmt.Errorf("agentproto: unsupported network %q", network)
	}
	if err := writeFull(w, []byte{dialRequestMagic0, dialRequestMagic1, code}); err != nil {
		return err
	}
	return WriteTarget(w, target)
}

// ReadRequest accepts both a v2 network-aware request and the legacy v1 TCP
// target. The magic is outside the valid legacy target-length range.
func ReadRequest(r io.Reader) (string, string, error) {
	var prefix [2]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return "", "", err
	}
	if prefix[0] != dialRequestMagic0 || prefix[1] != dialRequestMagic1 {
		target, err := readTargetLength(r, binary.BigEndian.Uint16(prefix[:]))
		return "tcp", target, err
	}

	var rawNetwork [1]byte
	if _, err := io.ReadFull(r, rawNetwork[:]); err != nil {
		return "", "", err
	}
	network := ""
	switch rawNetwork[0] {
	case dialNetworkTCP:
		network = "tcp"
	case dialNetworkUDP:
		network = "udp"
	default:
		return "", "", fmt.Errorf("agentproto: invalid network code %d", rawNetwork[0])
	}
	target, err := ReadTarget(r)
	return network, target, err
}

// WriteTarget 把目标地址以 2 字节大端长度前缀写入流。
func WriteTarget(w io.Writer, target string) error {
	if len(target) == 0 || len(target) > maxTargetLen {
		return fmt.Errorf("agentproto: invalid target length %d", len(target))
	}
	var hdr [2]byte
	binary.BigEndian.PutUint16(hdr[:], uint16(len(target)))
	if err := writeFull(w, hdr[:]); err != nil {
		return err
	}
	return writeFull(w, []byte(target))
}

// ReadTarget 读取由 WriteTarget 写入的目标地址。
func ReadTarget(r io.Reader) (string, error) {
	var hdr [2]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return "", err
	}
	return readTargetLength(r, binary.BigEndian.Uint16(hdr[:]))
}

func readTargetLength(r io.Reader, n uint16) (string, error) {
	if n == 0 || int(n) > maxTargetLen {
		return "", fmt.Errorf("agentproto: invalid target length %d", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

// PacketConn carries one connected UDP datagram per length-prefixed stream
// frame. It deliberately implements net.Conn so it can pass through the same
// Egress API without ever collapsing packet boundaries into an io.Copy stream.
type PacketConn struct {
	net.Conn
	remote  net.Addr
	readMu  sync.Mutex
	writeMu sync.Mutex
}

func NewPacketConn(conn net.Conn, remote net.Addr) *PacketConn {
	return &PacketConn{Conn: conn, remote: remote}
}

func (c *PacketConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	var header [2]byte
	if _, err := io.ReadFull(c.Conn, header[:]); err != nil {
		return 0, err
	}
	length := int(binary.BigEndian.Uint16(header[:]))
	readLength := length
	if readLength > len(p) {
		readLength = len(p)
	}
	if _, err := io.ReadFull(c.Conn, p[:readLength]); err != nil {
		return 0, err
	}
	if remaining := length - readLength; remaining > 0 {
		if _, err := io.CopyN(io.Discard, c.Conn, int64(remaining)); err != nil {
			return 0, err
		}
	}
	return readLength, nil
}

func (c *PacketConn) Write(p []byte) (int, error) {
	if len(p) > 65535 {
		return 0, fmt.Errorf("agentproto: UDP packet is too large: %d", len(p))
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	var header [2]byte
	binary.BigEndian.PutUint16(header[:], uint16(len(p)))
	if err := writeFull(c.Conn, header[:]); err != nil {
		return 0, err
	}
	if err := writeFull(c.Conn, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *PacketConn) RemoteAddr() net.Addr {
	if c.remote != nil {
		return c.remote
	}
	return c.Conn.RemoteAddr()
}

func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

// WriteStatus 回写 1 字节拨号状态。
func WriteStatus(w io.Writer, ok bool) error {
	b := byte(DialFail)
	if ok {
		b = DialOK
	}
	return writeFull(w, []byte{b})
}

// ReadStatus 读取 1 字节拨号状态。
func ReadStatus(r io.Reader) (bool, error) {
	var b [1]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return false, err
	}
	return b[0] == DialOK, nil
}
