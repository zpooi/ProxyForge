package wgcf

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

type Profile struct {
	PrivateKey    string
	AddressV4     string
	AddressV6     string
	PeerPublicKey string
	EndpointHost  string
	EndpointPort  int
	MTU           int
}

func ParseProfile(path string) (*Profile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var (
		privateKey    string
		addresses     []string
		peerPublicKey string
		endpoint      string
		mtu           = 1280
	)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if line == "[Interface]" || line == "[Peer]" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		switch key {
		case "PrivateKey":
			privateKey = val
		case "Address":
			addresses = append(addresses, val)
		case "PublicKey":
			peerPublicKey = val
		case "Endpoint":
			endpoint = val
		case "MTU":
			if n, err := strconv.Atoi(val); err == nil {
				mtu = n
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if privateKey == "" || peerPublicKey == "" || endpoint == "" {
		return nil, fmt.Errorf("missing required fields in %s", path)
	}

	host, port, err := splitHostPort(endpoint)
	if err != nil {
		return nil, fmt.Errorf("parse endpoint %q: %w", endpoint, err)
	}

	var v4, v6 string
	for _, a := range addresses {
		ip := a
		if idx := strings.Index(a, "/"); idx >= 0 {
			ip = a[:idx]
		}
		parsed := net.ParseIP(ip)
		if parsed == nil {
			continue
		}
		if parsed.To4() != nil {
			if v4 == "" {
				v4 = a
			}
		} else {
			if v6 == "" {
				v6 = a
			}
		}
	}

	return &Profile{
		PrivateKey:    privateKey,
		AddressV4:     v4,
		AddressV6:     v6,
		PeerPublicKey: peerPublicKey,
		EndpointHost:  host,
		EndpointPort:  port,
		MTU:           mtu,
	}, nil
}

func splitHostPort(endpoint string) (string, int, error) {
	endpoint = strings.TrimSpace(endpoint)
	if strings.HasPrefix(endpoint, "[") {
		idx := strings.Index(endpoint, "]")
		if idx < 0 {
			return "", 0, fmt.Errorf("malformed IPv6 endpoint")
		}
		host := endpoint[1:idx]
		rest := endpoint[idx+1:]
		if !strings.HasPrefix(rest, ":") {
			return "", 0, fmt.Errorf("missing port")
		}
		port, err := strconv.Atoi(rest[1:])
		if err != nil {
			return "", 0, err
		}
		return host, port, nil
	}
	host, portStr, err := net.SplitHostPort(endpoint)
	if err != nil {
		return "", 0, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}
