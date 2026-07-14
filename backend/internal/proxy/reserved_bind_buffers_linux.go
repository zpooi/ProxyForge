//go:build linux

package proxy

import (
	"net"

	"golang.org/x/sys/unix"
)

func forceUDPSocketBuffers(c *net.UDPConn) {
	raw, err := c.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		// These options require CAP_NET_ADMIN. Failure is intentionally ignored,
		// just like wireguard-go; the ordinary Set*Buffer calls still apply the
		// largest value permitted by net.core.{r,w}mem_max.
		_ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, wireGuardSocketBufferSize)
		_ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_SNDBUFFORCE, wireGuardSocketBufferSize)
	})
}
