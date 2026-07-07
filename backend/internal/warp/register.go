package warp

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/crypto/curve25519"
)

const (
	APIVersion       = "v0a884"
	masqueAPIVersion = "v0a4471"
	apiBase          = "https://api.cloudflareclient.com"
	userAgent        = "okhttp/3.12.1"

	keyTypeMasque = "secp256r1"
	tunTypeMasque = "masque"
)

type Account struct {
	PrivateKey    string
	PublicKey     string
	PeerPublicKey string
	EndpointHost  string
	ClientID      string
	AddressV4     string
	AddressV6     string
	AccessToken   string
	DeviceID      string
	License       string

	MasquePrivateKey     string
	MasqueEndpointPubKey string
	MasqueEndpointV4     string
	MasqueEndpointV6     string
}

type DeviceConfig struct {
	ClientID      string
	PeerPublicKey string
	EndpointHost  string
	AddressV4     string
	AddressV6     string
}

type MasqueConfig struct {
	PrivateKey     string
	EndpointPubKey string
	EndpointV4     string
	EndpointV6     string
	AddressV4      string
	AddressV6      string
}

type regRequest struct {
	Key       string `json:"key"`
	InstallID string `json:"install_id"`
	FcmToken  string `json:"fcm_token"`
	Tos       string `json:"tos"`
	Model     string `json:"model"`
	Type      string `json:"type"`
	Locale    string `json:"locale"`
}

type masqueUpdateRequest struct {
	Key     string `json:"key"`
	KeyType string `json:"key_type"`
	TunType string `json:"tunnel_type"`
	Name    string `json:"name,omitempty"`
}

type regResponse struct {
	ID     string `json:"id"`
	Token  string `json:"token"`
	Config struct {
		ClientID string `json:"client_id"`
		Peers    []struct {
			PublicKey string `json:"public_key"`
			Endpoint  struct {
				V4   string `json:"v4"`
				V6   string `json:"v6"`
				Host string `json:"host"`
			} `json:"endpoint"`
		} `json:"peers"`
		Interface struct {
			Addresses struct {
				V4 string `json:"v4"`
				V6 string `json:"v6"`
			} `json:"addresses"`
		} `json:"interface"`
	} `json:"config"`
	Account struct {
		License string `json:"license"`
	} `json:"account"`
}

type Client struct {
	http    *http.Client
	version string
}

func NewClient() *Client {
	return &Client{
		version: APIVersion,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				TLSClientConfig: &tls.Config{
					MinVersion: tls.VersionTLS10,
					MaxVersion: tls.VersionTLS12,
				},
			},
		},
	}
}

func (c *Client) Register(ctx context.Context) (*Account, error) {
	priv, pub, err := generateKeypair()
	if err != nil {
		return nil, fmt.Errorf("generate keypair: %w", err)
	}

	reqBody := regRequest{
		Key:       pub,
		InstallID: "",
		FcmToken:  "",
		Tos:       time.Now().Format(time.RFC3339Nano),
		Model:     "PC",
		Type:      "Android",
		Locale:    "en_US",
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/%s/reg", apiBase, c.version)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reg request: %w", err)
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("reg returned %d: %s", resp.StatusCode, string(respBytes))
	}

	var rr regResponse
	if err := json.Unmarshal(respBytes, &rr); err != nil {
		return nil, fmt.Errorf("parse reg response: %w", err)
	}
	if len(rr.Config.Peers) == 0 {
		return nil, fmt.Errorf("reg response has no peers")
	}

	peer := rr.Config.Peers[0]
	host := peer.Endpoint.Host
	if host == "" {
		host = peer.Endpoint.V4
	}

	return &Account{
		PrivateKey:    priv,
		PublicKey:     pub,
		PeerPublicKey: peer.PublicKey,
		EndpointHost:  host,
		ClientID:      rr.Config.ClientID,
		AddressV4:     stripMask(rr.Config.Interface.Addresses.V4),
		AddressV6:     stripMask(rr.Config.Interface.Addresses.V6),
		AccessToken:   rr.Token,
		DeviceID:      rr.ID,
		License:       rr.Account.License,
	}, nil
}

func (c *Client) EnrollMasque(ctx context.Context, deviceID, accessToken, name string) (*MasqueConfig, error) {
	if deviceID == "" || accessToken == "" {
		return nil, fmt.Errorf("device id and access token are required")
	}
	privateDER, publicDER, err := generateECKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generate MASQUE keypair: %w", err)
	}

	reqBody := masqueUpdateRequest{
		Key:     base64.StdEncoding.EncodeToString(publicDER),
		KeyType: keyTypeMasque,
		TunType: tunTypeMasque,
		Name:    name,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := fmt.Sprintf("%s/%s/reg/%s", apiBase, masqueAPIVersion, deviceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	setMasqueHeaders(req)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("MASQUE enroll request: %w", err)
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("MASQUE enroll returned %d: %s", resp.StatusCode, string(respBytes))
	}

	var rr regResponse
	if err := json.Unmarshal(respBytes, &rr); err != nil {
		return nil, fmt.Errorf("parse MASQUE enroll response: %w", err)
	}
	if len(rr.Config.Peers) == 0 {
		return nil, fmt.Errorf("MASQUE enroll response has no peers")
	}
	peer := rr.Config.Peers[0]
	return &MasqueConfig{
		PrivateKey:     base64.StdEncoding.EncodeToString(privateDER),
		EndpointPubKey: peer.PublicKey,
		EndpointV4:     cleanEndpointIP(peer.Endpoint.V4),
		EndpointV6:     cleanEndpointIP(peer.Endpoint.V6),
		AddressV4:      stripMask(rr.Config.Interface.Addresses.V4),
		AddressV6:      stripMask(rr.Config.Interface.Addresses.V6),
	}, nil
}

func (c *Client) GetDeviceConfig(ctx context.Context, deviceID, accessToken string) (*DeviceConfig, error) {
	if deviceID == "" || accessToken == "" {
		return nil, fmt.Errorf("device id and access token are required")
	}
	url := fmt.Sprintf("%s/%s/reg/%s", apiBase, c.version, deviceID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("device request: %w", err)
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device returned %d: %s", resp.StatusCode, string(respBytes))
	}

	var rr regResponse
	if err := json.Unmarshal(respBytes, &rr); err != nil {
		return nil, fmt.Errorf("parse device response: %w", err)
	}
	cfg := &DeviceConfig{
		ClientID:  rr.Config.ClientID,
		AddressV4: stripMask(rr.Config.Interface.Addresses.V4),
		AddressV6: stripMask(rr.Config.Interface.Addresses.V6),
	}
	if len(rr.Config.Peers) > 0 {
		peer := rr.Config.Peers[0]
		cfg.PeerPublicKey = peer.PublicKey
		cfg.EndpointHost = peer.Endpoint.Host
		if cfg.EndpointHost == "" {
			cfg.EndpointHost = peer.Endpoint.V4
		}
	}
	return cfg, nil
}

func setMasqueHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "WARP for Android")
	req.Header.Set("CF-Client-Version", "a-6.35-4471")
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("Connection", "Keep-Alive")
}

func generateKeypair() (privB64, pubB64 string, err error) {
	var priv [32]byte
	if _, err := rand.Read(priv[:]); err != nil {
		return "", "", err
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(priv[:]),
		base64.StdEncoding.EncodeToString(pub), nil
}

func generateECKeyPair() ([]byte, []byte, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	privateDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return nil, nil, err
	}
	return privateDER, publicDER, nil
}

func stripMask(addr string) string {
	for i := 0; i < len(addr); i++ {
		if addr[i] == '/' {
			return addr[:i]
		}
	}
	return addr
}

func cleanEndpointIP(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	return strings.Trim(raw, "[]")
}
