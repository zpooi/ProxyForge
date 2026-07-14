package proxy

import (
	"errors"
	"net"
	"strings"
	"time"
)

const (
	// A broken destination must not make one client connection walk the entire
	// WARP pool. Three candidates preserve useful failover while bounding the
	// amount of work and the delay seen by the caller.
	maxProxyDialAttempts = 3
	proxyDialAttemptTTL  = 5 * time.Second
	proxyDialTotalTTL    = 12 * time.Second
)

// isPermanentTargetDialError reports errors that describe the requested
// destination rather than the selected tunnel. Trying the same missing name or
// refused address through every egress only adds latency, and these errors must
// not count against tunnel health.
func isPermanentTargetDialError(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return true
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{
		"no such host",
		"name does not resolve",
		"connection refused",
		"address family not supported",
		"no suitable address",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}
