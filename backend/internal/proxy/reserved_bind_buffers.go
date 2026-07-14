package proxy

import "net"

// wireGuardSocketBufferSize is the value used by wireguard-go's native UDP
// bind. Platforms may clamp it to their configured maximum; Linux gets an
// additional best-effort SO_*BUFFORCE attempt when the process has permission.
const wireGuardSocketBufferSize = 7 << 20

func tuneUDPSocketBuffers(c *net.UDPConn) {
	if c == nil {
		return
	}
	_ = c.SetReadBuffer(wireGuardSocketBufferSize)
	_ = c.SetWriteBuffer(wireGuardSocketBufferSize)
	forceUDPSocketBuffers(c)
}
