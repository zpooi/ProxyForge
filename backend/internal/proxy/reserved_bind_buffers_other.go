//go:build !linux

package proxy

import "net"

func forceUDPSocketBuffers(*net.UDPConn) {}
