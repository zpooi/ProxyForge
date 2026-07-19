package proxy

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// newSelfSignedTLSConfig 为入站代理监听生成一张内存自签证书。这张证书只用来把
// 客户端↔代理这一跳套进 TLS，好让审查中间盒读不到明文的 CONNECT 主机名；客户端
// 用 skip-cert-verify 连接，所以不需要信任证书身份。serverName 非空且不是 IP 时
// 作为 SAN 写入，方便有需要的客户端仍能按主机名校验。
func newSelfSignedTLSConfig(serverName string) (*tls.Config, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate tls key: %w", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "ProxyForge"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if serverName != "" {
		if ip := net.ParseIP(serverName); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, serverName)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, fmt.Errorf("create tls cert: %w", err)
	}
	cert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// newProxyTLSConfig loads a trusted certificate when file paths are supplied.
// With no files configured it keeps the self-signed compatibility mode, but
// the listener still requires TLS; callers must never fall back to plaintext.
func newProxyTLSConfig(serverName, certFile, keyFile string) (*tls.Config, error) {
	certFile = strings.TrimSpace(certFile)
	keyFile = strings.TrimSpace(keyFile)
	if certFile == "" && keyFile == "" {
		return newDynamicProxyTLSConfig(serverName)
	}
	if certFile == "" || keyFile == "" {
		return nil, fmt.Errorf("both proxy TLS certificate and key files are required")
	}
	reloader := &fileCertificateReloader{serverName: serverName, certFile: certFile, keyFile: keyFile}
	if _, err := reloader.load(); err != nil {
		return nil, err
	}
	return &tls.Config{
		GetCertificate: reloader.getCertificate,
		MinVersion:     tls.VersionTLS12,
	}, nil
}

type dynamicCertificateResolver struct {
	mu         sync.Mutex
	serverName string
	fallback   tls.Certificate
	reloaders  map[string]*fileCertificateReloader
}

func newDynamicProxyTLSConfig(serverName string) (*tls.Config, error) {
	fallbackConfig, err := newSelfSignedTLSConfig(serverName)
	if err != nil {
		return nil, err
	}
	resolver := &dynamicCertificateResolver{
		serverName: strings.TrimSpace(serverName),
		fallback:   fallbackConfig.Certificates[0],
		reloaders:  make(map[string]*fileCertificateReloader),
	}
	return &tls.Config{
		GetCertificate: resolver.getCertificate,
		MinVersion:     tls.VersionTLS12,
	}, nil
}

func (r *dynamicCertificateResolver) getCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	host := r.serverName
	if hello != nil && strings.TrimSpace(hello.ServerName) != "" {
		host = hello.ServerName
	}
	host = safeTLSCertificateHost(host)
	if host == "" {
		return &r.fallback, nil
	}
	certFile, keyFile := ResolveTLSCredentialFiles(host, "", "")
	if certFile == "" || keyFile == "" {
		return &r.fallback, nil
	}

	r.mu.Lock()
	reloader := r.reloaders[host]
	if reloader == nil || reloader.certFile != certFile || reloader.keyFile != keyFile {
		reloader = &fileCertificateReloader{serverName: host, certFile: certFile, keyFile: keyFile}
		r.reloaders[host] = reloader
		log.Printf("发现 trusted TLS certificate for %s at %s", host, certFile)
	}
	r.mu.Unlock()
	return reloader.getCertificate(hello)
}

type certificateFileStamp struct {
	modTime int64
	size    int64
}

type fileCertificateReloader struct {
	mu          sync.Mutex
	serverName  string
	certFile    string
	keyFile     string
	certStamp   certificateFileStamp
	keyStamp    certificateFileStamp
	certificate *tls.Certificate
}

func (r *fileCertificateReloader) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	return r.load()
}

func (r *fileCertificateReloader) load() (*tls.Certificate, error) {
	certInfo, certErr := os.Stat(r.certFile)
	keyInfo, keyErr := os.Stat(r.keyFile)

	r.mu.Lock()
	defer r.mu.Unlock()
	if certErr != nil || keyErr != nil {
		if r.certificate != nil {
			return r.certificate, nil
		}
		if certErr != nil {
			return nil, fmt.Errorf("stat proxy TLS certificate: %w", certErr)
		}
		return nil, fmt.Errorf("stat proxy TLS key: %w", keyErr)
	}
	certStamp := certificateFileStamp{modTime: certInfo.ModTime().UnixNano(), size: certInfo.Size()}
	keyStamp := certificateFileStamp{modTime: keyInfo.ModTime().UnixNano(), size: keyInfo.Size()}
	if r.certificate != nil && certStamp == r.certStamp && keyStamp == r.keyStamp {
		return r.certificate, nil
	}

	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		if r.certificate != nil {
			return r.certificate, nil
		}
		return nil, fmt.Errorf("load proxy TLS certificate: %w", err)
	}
	if err := validateProxyCertificate(&cert, r.serverName, time.Now()); err != nil {
		if r.certificate != nil {
			return r.certificate, nil
		}
		return nil, err
	}
	r.certStamp = certStamp
	r.keyStamp = keyStamp
	r.certificate = &cert
	return r.certificate, nil
}

func validateProxyCertificate(cert *tls.Certificate, serverName string, now time.Time) error {
	if cert == nil || len(cert.Certificate) == 0 {
		return fmt.Errorf("proxy TLS certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return fmt.Errorf("parse proxy TLS certificate: %w", err)
	}
	if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
		return fmt.Errorf("proxy TLS certificate is not valid at the current time")
	}
	serverName = strings.TrimSpace(serverName)
	if serverName != "" {
		if err := leaf.VerifyHostname(serverName); err != nil {
			return fmt.Errorf("proxy TLS certificate does not cover %q: %w", serverName, err)
		}
	}
	return nil
}

// ResolveTLSCredentialFiles prefers explicit environment/configuration paths,
// then discovers certificates from common BaoTa and certbot locations.
func ResolveTLSCredentialFiles(serverName, certFile, keyFile string) (string, string) {
	certFile = strings.TrimSpace(certFile)
	keyFile = strings.TrimSpace(keyFile)
	if certFile != "" || keyFile != "" {
		return certFile, keyFile
	}
	host := safeTLSCertificateHost(serverName)
	if host == "" {
		return "", ""
	}
	candidates := [][2]string{
		{
			filepath.FromSlash("/www/server/panel/vhost/cert/" + host + "/fullchain.pem"),
			filepath.FromSlash("/www/server/panel/vhost/cert/" + host + "/privkey.pem"),
		},
		{
			filepath.FromSlash("/etc/letsencrypt/live/" + host + "/fullchain.pem"),
			filepath.FromSlash("/etc/letsencrypt/live/" + host + "/privkey.pem"),
		},
	}
	for _, pair := range candidates {
		if readableTLSFile(pair[0]) && readableTLSFile(pair[1]) {
			return pair[0], pair[1]
		}
	}
	return "", ""
}

func safeTLSCertificateHost(serverName string) string {
	host := strings.ToLower(strings.TrimSpace(serverName))
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	host = strings.Trim(host, "[]")
	if host == "" || net.ParseIP(host) != nil || strings.Contains(host, "..") || strings.ContainsAny(host, `/\\`) {
		return ""
	}
	for _, r := range host {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-') {
			return ""
		}
	}
	return host
}

func readableTLSFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	return f.Close() == nil
}
