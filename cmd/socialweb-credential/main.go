package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/execplugin"
)

const (
	envDeviceKey = "SOCIALWEB_DEVICE_KEY"
	envMintURL   = "SOCIALWEB_MINT_URL"
	envCachePath = "SOCIALWEB_CREDENTIAL_CACHE"
	envTimeout   = "SOCIALWEB_MINT_TIMEOUT"
)

func main() {
	ctx := context.Background()

	mintURL := os.Getenv(envMintURL)
	if mintURL == "" {
		fail(fmt.Errorf("%s is not set", envMintURL))
	}

	client := &httpMinter{
		URL:    mintURL,
		Client: &http.Client{Timeout: timeout()},
	}

	body, code, err := execplugin.Run(ctx, client, os.Getenv(envDeviceKey), execplugin.Options{
		CachePath: os.Getenv(envCachePath),
	})
	if err != nil {
		fail(err)
	}
	if code != execplugin.ExitOK {
		fail(fmt.Errorf("credential unavailable (exit %d)", code))
	}
	fmt.Fprintln(os.Stdout, body)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "socialweb-credential:", err)
	os.Exit(execplugin.ExitFailure)
}

func timeout() time.Duration {
	raw := os.Getenv(envTimeout)
	if raw == "" {
		return 10 * time.Second
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 10 * time.Second
	}
	return d
}

type httpMinter struct {
	URL    string
	Client *http.Client
}

func (m *httpMinter) Mint(ctx context.Context, deviceKey string) (execplugin.Credential, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.URL,
		bytes.NewReader([]byte(`{"name":""}`)))
	if err != nil {
		return execplugin.Credential{}, err
	}
	req.Header.Set("Authorization", "Bearer "+deviceKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.Client.Do(req)
	if err != nil {
		return execplugin.Credential{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return execplugin.Credential{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return execplugin.Credential{}, fmt.Errorf("mint returned %d", resp.StatusCode)
	}

	var cred execplugin.Credential
	if err := json.Unmarshal(body, &cred); err != nil {
		return execplugin.Credential{}, fmt.Errorf("mint returned a body that is not an ExecCredential: %w", err)
	}
	return cred, nil
}
