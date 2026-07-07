package wgcf

import (
	"bufio"
	"os"
	"strings"
)

type Account struct {
	AccessToken string
	DeviceID    string
	LicenseKey  string
	PrivateKey  string
}

func ParseAccount(path string) (*Account, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var a Account
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, `"'`)
		switch key {
		case "access_token":
			a.AccessToken = val
		case "device_id":
			a.DeviceID = val
		case "license_key":
			a.LicenseKey = val
		case "private_key":
			a.PrivateKey = val
		}
	}
	return &a, scanner.Err()
}
