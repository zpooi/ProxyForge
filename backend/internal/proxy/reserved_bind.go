package proxy

import (
	"errors"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.zx2c4.com/wireguard/conn"
)

type reservedBind struct {
	mu       sync.Mutex
	ipv4     *net.UDPConn
	ipv6     *net.UDPConn
	reserved map[netip.AddrPort][3]byte

	txPackets atomic.Uint64
	rxPackets atomic.Uint64
	txBytes   atomic.Uint64
	rxBytes   atomic.Uint64
	lastTx    atomic.Int64
	lastRx    atomic.Int64
}

type reservedEndpoint struct {
	dst netip.AddrPort
}

func newReservedBind() *reservedBind {
	return &reservedBind{reserved: make(map[netip.AddrPort][3]byte)}
}

func (b *reservedBind) SetReservedForEndpoint(dst netip.AddrPort, reserved [3]byte) {
	b.reserved[dst] = reserved
}

func (b *reservedBind) Open(port uint16) ([]conn.ReceiveFunc, uint16, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.ipv4 != nil || b.ipv6 != nil {
		return nil, 0, conn.ErrBindAlreadyOpen
	}

	requested := int(port)
	ipv4Conn, actualPort, err4 := listenUDP("udp4", requested)
	if err4 == nil {
		b.ipv4 = ipv4Conn
		requested = actualPort
	}

	ipv6Conn, actualPort6, err6 := listenUDP("udp6", requested)
	if err6 == nil {
		b.ipv6 = ipv6Conn
		if actualPort == 0 {
			actualPort = actualPort6
		}
	}

	var fns []conn.ReceiveFunc
	if b.ipv4 != nil {
		fns = append(fns, b.receive(b.ipv4))
	}
	if b.ipv6 != nil {
		fns = append(fns, b.receive(b.ipv6))
	}
	if len(fns) == 0 {
		if err4 != nil && !errors.Is(err4, syscall.EAFNOSUPPORT) {
			return nil, 0, err4
		}
		if err6 != nil {
			return nil, 0, err6
		}
		return nil, 0, syscall.EAFNOSUPPORT
	}
	return fns, uint16(actualPort), nil
}

func listenUDP(network string, port int) (*net.UDPConn, int, error) {
	addr, err := net.ResolveUDPAddr(network, ":"+strconv.Itoa(port))
	if err != nil {
		return nil, 0, err
	}
	c, err := net.ListenUDP(network, addr)
	if err != nil {
		return nil, 0, err
	}
	// Match wireguard-go's native bind. The reserved-byte wrapper used for
	// Cloudflare WARP must not fall back to the OS's much smaller default UDP
	// queues: short bursts can fill them, drop encrypted packets, and make the
	// inner TCP connection collapse to a low congestion window.
	tuneUDPSocketBuffers(c)
	local := c.LocalAddr().(*net.UDPAddr)
	return c, local.Port, nil
}

func (b *reservedBind) receive(c *net.UDPConn) conn.ReceiveFunc {
	return func(packets [][]byte, sizes []int, eps []conn.Endpoint) (int, error) {
		n, addr, err := c.ReadFromUDPAddrPort(packets[0])
		if err != nil {
			return 0, err
		}
		if n > 3 {
			clear(packets[0][1:4])
		}
		b.rxPackets.Add(1)
		b.rxBytes.Add(uint64(n))
		b.lastRx.Store(time.Now().Unix())
		sizes[0] = n
		eps[0] = &reservedEndpoint{dst: addr}
		return 1, nil
	}
}

func (b *reservedBind) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()

	var err error
	if b.ipv4 != nil {
		err = b.ipv4.Close()
		b.ipv4 = nil
	}
	if b.ipv6 != nil {
		if e := b.ipv6.Close(); err == nil {
			err = e
		}
		b.ipv6 = nil
	}
	return err
}

func (b *reservedBind) SetMark(uint32) error { return nil }

func (b *reservedBind) Send(packets [][]byte, endpoint conn.Endpoint) error {
	ep, ok := endpoint.(*reservedEndpoint)
	if !ok {
		return conn.ErrWrongEndpointType
	}

	b.mu.Lock()
	c := b.ipv4
	if ep.dst.Addr().Is6() {
		c = b.ipv6
	}
	reserved, hasReserved := b.reserved[ep.dst]
	b.mu.Unlock()
	if c == nil {
		return syscall.EAFNOSUPPORT
	}

	for _, packet := range packets {
		if hasReserved && len(packet) > 3 {
			copy(packet[1:4], reserved[:])
		}
		if _, err := c.WriteToUDPAddrPort(packet, ep.dst); err != nil {
			return err
		}
		b.txPackets.Add(1)
		b.txBytes.Add(uint64(len(packet)))
		b.lastTx.Store(time.Now().Unix())
	}
	return nil
}

func (b *reservedBind) ParseEndpoint(s string) (conn.Endpoint, error) {
	addr, err := netip.ParseAddrPort(s)
	if err != nil {
		return nil, err
	}
	return &reservedEndpoint{dst: addr}, nil
}

func (b *reservedBind) BatchSize() int { return 1 }

func (b *reservedBind) Stats() string {
	return "udp_tx_packets=" + strconv.FormatUint(b.txPackets.Load(), 10) +
		" udp_rx_packets=" + strconv.FormatUint(b.rxPackets.Load(), 10) +
		" udp_tx_bytes=" + strconv.FormatUint(b.txBytes.Load(), 10) +
		" udp_rx_bytes=" + strconv.FormatUint(b.rxBytes.Load(), 10) +
		" udp_last_tx=" + formatUnixTime(b.lastTx.Load()) +
		" udp_last_rx=" + formatUnixTime(b.lastRx.Load())
}

func formatUnixTime(sec int64) string {
	if sec <= 0 {
		return "never"
	}
	return time.Unix(sec, 0).Format(time.RFC3339)
}

func (e *reservedEndpoint) ClearSrc() {}

func (e *reservedEndpoint) SrcToString() string { return "" }

func (e *reservedEndpoint) DstToString() string { return e.dst.String() }

func (e *reservedEndpoint) DstToBytes() []byte {
	b, _ := e.dst.MarshalBinary()
	return b
}

func (e *reservedEndpoint) DstIP() netip.Addr { return e.dst.Addr() }

func (e *reservedEndpoint) SrcIP() netip.Addr { return netip.Addr{} }
