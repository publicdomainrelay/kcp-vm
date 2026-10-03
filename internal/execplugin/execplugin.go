package execplugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	ExitOK      = 0
	ExitFailure = 3
)

type Credential struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Spec       json.RawMessage `json:"spec,omitempty"`
	Status     CredentialState `json:"status"`
}

type CredentialState struct {
	Token               string `json:"token"`
	ExpirationTimestamp string `json:"expirationTimestamp"`
}

func (c Credential) Remaining(now time.Time) (time.Duration, error) {
	if c.Status.ExpirationTimestamp == "" {
		return 0, errors.New("execplugin: the credential carries no expirationTimestamp; " +
			"printing it would make client-go cache the token for the life of the process, " +
			"so a short-lived token would keep being presented after it expired")
	}
	ts, err := time.Parse(time.RFC3339, c.Status.ExpirationTimestamp)
	if err != nil {
		return 0, fmt.Errorf("execplugin: expirationTimestamp is not RFC3339: %w", err)
	}
	return ts.Sub(now), nil
}

type Minter interface {
	Mint(ctx context.Context, deviceKey string) (Credential, error)
}

type Options struct {
	CachePath string

	Skew time.Duration

	CacheTTL time.Duration

	Now func() time.Time
}

const (
	DefaultSkew     = 30 * time.Second
	DefaultCacheTTL = 15 * time.Minute
)

type cached struct {
	DeviceKey string     `json:"deviceKey"`
	Body      Credential `json:"body"`
	FetchedAt time.Time  `json:"fetchedAt"`
}

func Run(ctx context.Context, m Minter, deviceKey string, opts Options) (string, int, error) {
	if deviceKey == "" {
		return "", ExitFailure, errors.New("execplugin: no device key in the environment")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Skew == 0 {
		opts.Skew = DefaultSkew
	}
	if opts.CacheTTL == 0 {
		opts.CacheTTL = DefaultCacheTTL
	}
	now := opts.Now()

	if body, ok := readCache(opts.CachePath, deviceKey, now, opts); ok {
		return body, ExitOK, nil
	}

	cred, err := m.Mint(ctx, deviceKey)
	if err != nil {
		return "", ExitFailure, fmt.Errorf("execplugin: mint failed: %w", err)
	}
	remaining, err := cred.Remaining(now)
	if err != nil {
		return "", ExitFailure, err
	}
	if cred.Status.Token == "" {
		return "", ExitFailure, errors.New("execplugin: the mint returned an empty token; " +
			"printing it would make client-go send it and repeat SILENTLY")
	}
	if remaining <= 0 {
		return "", ExitFailure, fmt.Errorf("execplugin: the mint returned a credential that "+
			"is already expired (%s)", cred.Status.ExpirationTimestamp)
	}

	raw, err := json.Marshal(cred)
	if err != nil {
		return "", ExitFailure, fmt.Errorf("execplugin: marshal: %w", err)
	}
	writeCache(opts.CachePath, deviceKey, cred, now)
	return string(raw), ExitOK, nil
}

func readCache(path, deviceKey string, now time.Time, opts Options) (string, bool) {
	if path == "" {
		return "", false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	var c cached
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", false
	}
	if c.DeviceKey != deviceKey {
		return "", false
	}
	if now.Sub(c.FetchedAt) > opts.CacheTTL {
		return "", false
	}
	remaining, err := c.Body.Remaining(now)
	if err != nil || remaining <= opts.Skew {
		return "", false
	}
	if c.Body.Status.Token == "" {
		return "", false
	}
	out, err := json.Marshal(c.Body)
	if err != nil {
		return "", false
	}
	return string(out), true
}

func writeCache(path, deviceKey string, body Credential, now time.Time) {
	if path == "" {
		return
	}
	raw, err := json.Marshal(cached{DeviceKey: deviceKey, Body: body, FetchedAt: now})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
