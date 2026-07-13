package proxy

import (
	"crypto/tls"
	"testing"
	"time"
)

func TestResolveTLSCredentialFilesPrefersExplicitPaths(t *testing.T) {
	certFile, keyFile := ResolveTLSCredentialFiles("proxy.example.test", " cert.pem ", " key.pem ")
	if certFile != "cert.pem" || keyFile != "key.pem" {
		t.Fatalf("resolved files = %q, %q", certFile, keyFile)
	}
}

func TestSafeTLSCertificateHostRejectsUnsafeNames(t *testing.T) {
	for _, host := range []string{"", "../example.com", "example.com/path", "198.51.100.1", "bad name.example"} {
		if got := safeTLSCertificateHost(host); got != "" {
			t.Errorf("safeTLSCertificateHost(%q) = %q", host, got)
		}
	}
	if got := safeTLSCertificateHost("Proxy.Example.COM"); got != "proxy.example.com" {
		t.Fatalf("normalized host = %q", got)
	}
}

func TestValidateProxyCertificateChecksHostnameAndValidity(t *testing.T) {
	config, err := newSelfSignedTLSConfig("proxy.example.test")
	if err != nil {
		t.Fatal(err)
	}
	cert := &config.Certificates[0]
	if err := validateProxyCertificate(cert, "proxy.example.test", time.Now()); err != nil {
		t.Fatalf("valid certificate rejected: %v", err)
	}
	if err := validateProxyCertificate(cert, "wrong.example.test", time.Now()); err == nil {
		t.Fatal("hostname mismatch was accepted")
	}
	if err := validateProxyCertificate(cert, "proxy.example.test", time.Now().Add(20*365*24*time.Hour)); err == nil {
		t.Fatal("expired certificate was accepted")
	}
}

func TestProxyTLSConfigRejectsPartialFileConfiguration(t *testing.T) {
	if _, err := newProxyTLSConfig("proxy.example.test", "cert.pem", ""); err == nil {
		t.Fatal("partial TLS file configuration was accepted")
	}
}

func TestDynamicProxyTLSConfigFallsBackWhenNoTrustedFileExists(t *testing.T) {
	config, err := newProxyTLSConfig("proxy.example.test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if config.GetCertificate == nil {
		t.Fatal("dynamic TLS config has no certificate resolver")
	}
	cert, err := config.GetCertificate(&tls.ClientHelloInfo{ServerName: "missing.example.test"})
	if err != nil || cert == nil {
		t.Fatalf("fallback certificate = %v, err=%v", cert, err)
	}
}
