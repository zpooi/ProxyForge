package test

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/zpooi/ProxyForge/backend/internal/models"
)

const (
	latencyProbes     = 3
	traceURL          = "https://www.cloudflare.com/cdn-cgi/trace"
	speedURLFormat    = "https://speed.cloudflare.com/__down?bytes=%d"
	speedTestBytes    = 1_000_000
	speedProbes       = 2
	speedProbeTimeout = 15 * time.Second
	defaultProxyPort  = 7843
)

type Result struct {
	PublicIP   string
	Colo       string
	Country    string
	LatencyMs  int
	SpeedBps   int
	PacketLoss float64
	Score      float64
	Err        error
}

type Tester struct {
	proxyPort int
	password  string
	proxyTLS  bool
}

func NewTester(proxyPort int, password string, proxyTLS ...bool) *Tester {
	if proxyPort <= 0 {
		proxyPort = defaultProxyPort
	}
	tlsEnabled := len(proxyTLS) > 0 && proxyTLS[0]
	return &Tester{proxyPort: proxyPort, password: password, proxyTLS: tlsEnabled}
}

func (t *Tester) TestAccount(ctx context.Context, a *models.Account) Result {
	if a.Tag == "" {
		return Result{Err: fmt.Errorf("account has no tag")}
	}
	client := t.proxyClient(a.Tag, t.password, 30*time.Second)

	var res Result

	// Warm once so startup jitter and DNS cache misses do not dominate the
	// displayed latency. User traffic benefits from the same warm tunnel.
	ip, colo, country, _ := t.fetchTrace(ctx, client)
	best, loss, ip, colo, country, err := t.measureProxyLatency(ctx, client, ip, colo, country)
	res.LatencyMs = best
	res.PacketLoss = loss
	if err != nil {
		res.Err = err
		return res
	}
	res.PublicIP = ip
	res.Colo = colo
	res.Country = country
	res.SpeedBps = measureSpeed(ctx, client)
	res.Score = Score(res.LatencyMs, res.PacketLoss, res.SpeedBps)
	return res
}

func (t *Tester) ProbeProxy(ctx context.Context, username, password string) Result {
	if username == "" {
		return Result{Err: fmt.Errorf("proxy username is empty")}
	}
	client := t.proxyClient(username, password, 10*time.Second)
	start := time.Now()
	ip, colo, country, err := t.fetchTrace(ctx, client)
	if err != nil {
		return Result{LatencyMs: 9999, PacketLoss: 1, Err: err}
	}
	return Result{
		PublicIP:   ip,
		Colo:       colo,
		Country:    country,
		LatencyMs:  int(time.Since(start).Milliseconds()),
		PacketLoss: 0,
	}
}

func (t *Tester) proxyClient(username, password string, timeout time.Duration) *http.Client {
	proxyURL := &url.URL{
		Scheme: "http",
		User:   url.UserPassword(username, password),
		Host:   fmt.Sprintf("127.0.0.1:%d", t.proxyPort),
	}
	if t.proxyTLS {
		proxyURL.Scheme = "https"
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{
				MinVersion:         tls.VersionTLS12,
				InsecureSkipVerify: t.proxyTLS, // loopback health probe; public clients still verify
			},
			DialContext: (&net.Dialer{
				Timeout: 5 * time.Second,
			}).DialContext,
		},
		Timeout: timeout,
	}
}

func (t *Tester) measureProxyLatency(ctx context.Context, client *http.Client, ip, colo, country string) (int, float64, string, string, string, error) {
	best := time.Duration(0)
	failures := 0
	var lastErr error
	for i := 0; i < latencyProbes; i++ {
		start := time.Now()
		nextIP, nextColo, nextCountry, err := t.fetchTrace(ctx, client)
		elapsed := time.Since(start)
		if err != nil {
			failures++
			lastErr = err
			continue
		}
		if nextIP != "" {
			ip = nextIP
		}
		if nextColo != "" {
			colo = nextColo
		}
		if nextCountry != "" {
			country = nextCountry
		}
		if best == 0 || elapsed < best {
			best = elapsed
		}
	}
	if best == 0 {
		return 9999, 1.0, ip, colo, country, lastErr
	}
	return int(best.Milliseconds()), float64(failures) / float64(latencyProbes), ip, colo, country, nil
}

func (t *Tester) fetchTrace(ctx context.Context, client *http.Client) (string, string, string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", traceURL, nil)
	if err != nil {
		return "", "", "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return "", "", "", fmt.Errorf("trace returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", "", err
	}
	ip, colo, country := "", "", ""
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "ip=") {
			ip = strings.TrimPrefix(line, "ip=")
		} else if strings.HasPrefix(line, "colo=") {
			colo = strings.TrimPrefix(line, "colo=")
		} else if strings.HasPrefix(line, "loc=") {
			country = strings.TrimPrefix(line, "loc=")
		}
	}
	if ip == "" {
		return "", "", "", fmt.Errorf("no ip in trace response")
	}
	return ip, colo, country, nil
}

func measureSpeed(ctx context.Context, client *http.Client) int {
	best := 0
	for i := 0; i < speedProbes; i++ {
		speed := measureSpeedOnce(ctx, client)
		if speed > best {
			best = speed
		}
	}
	return best
}

func measureSpeedOnce(ctx context.Context, client *http.Client) int {
	speedCtx, cancel := context.WithTimeout(ctx, speedProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(speedCtx, "GET", fmt.Sprintf(speedURLFormat, speedTestBytes), nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Accept-Encoding", "identity")
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return 0
	}

	buf := make([]byte, 64*1024)
	n, _ := io.CopyBuffer(io.Discard, resp.Body, buf)
	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 || n <= 0 {
		return 0
	}
	return int(float64(n) / elapsed)
}

func (t *Tester) RunBatch(ctx context.Context, accounts []*models.Account, concurrency int) map[int64]Result {
	if concurrency < 1 {
		concurrency = 5
	}
	results := make(map[int64]Result)
	var mu sync.Mutex
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for _, a := range accounts {
		wg.Add(1)
		go func(a *models.Account) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			r := t.TestAccount(ctx, a)
			mu.Lock()
			results[a.ID] = r
			mu.Unlock()
		}(a)
	}
	wg.Wait()
	return results
}
