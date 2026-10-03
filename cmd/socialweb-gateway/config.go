package main

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"
)

const (
	envAddr            = "SOCIALWEB_GATEWAY_ADDR"
	envAudiences       = "SOCIALWEB_GATEWAY_AUDIENCES"
	envKCPHost         = "SOCIALWEB_KCP_HOST"
	envKCPKubeconfig   = "SOCIALWEB_KCP_KUBECONFIG"
	envMintTTL         = "SOCIALWEB_GATEWAY_MINT_TTL"
	envMintFailureHold = "SOCIALWEB_GATEWAY_MINT_FAILURE_HOLD"
	envDirectoryTTL    = "SOCIALWEB_GATEWAY_DIRECTORY_CACHE_TTL"
	envShutdownTimeout = "SOCIALWEB_GATEWAY_SHUTDOWN_TIMEOUT"
	envLogLevel        = "SOCIALWEB_GATEWAY_LOG_LEVEL"

	envPLCURL = "SOCIALWEB_GATEWAY_PLC_URL"

	envMintKubeconfigs = "SOCIALWEB_GATEWAY_MINT_KUBECONFIGS"

	envDevkeyRegistry = "SOCIALWEB_GATEWAY_DEVKEY_REGISTRY"

	envDeviceKeyAudience    = "SOCIALWEB_GATEWAY_DEVICE_KEY_AUDIENCE"
	envDeviceKeyTTL         = "SOCIALWEB_GATEWAY_DEVICE_KEY_TTL"
	envDeviceKeyExecCommand = "SOCIALWEB_GATEWAY_DEVICE_KEY_EXEC_COMMAND"
	envDeviceKeyExecEnvName = "SOCIALWEB_GATEWAY_DEVICE_KEY_EXEC_ENV_NAME"
)

const DevkeyRegistryNamespace = "default"

const DefaultDirectoryCacheTTL = 60 * time.Second

const DefaultShutdownTimeout = 30 * time.Second

type Config struct {
	Addr string

	Audiences []string

	KCPHost string

	KCPKubeconfig string

	MintTTL         time.Duration
	MintFailureHold time.Duration

	DirectoryCacheTTL time.Duration

	PLCURL string

	ShutdownTimeout time.Duration

	MintKubeconfigs string

	DevkeyRegistry string

	DeviceKeyAudience string

	DeviceKeyTTL time.Duration

	DeviceKeyExecCommand string
	DeviceKeyExecEnvName string

	LogLevel slog.Level
}

func Load() (Config, error) { return load(os.Getenv) }

func load(get func(string) string) (Config, error) {
	cfg := Config{
		Addr:            strings.TrimSpace(get(envAddr)),
		KCPHost:         strings.TrimSpace(get(envKCPHost)),
		KCPKubeconfig:   strings.TrimSpace(get(envKCPKubeconfig)),
		PLCURL:          strings.TrimSpace(get(envPLCURL)),
		MintKubeconfigs: strings.TrimSpace(get(envMintKubeconfigs)),
		DevkeyRegistry:  strings.TrimSpace(get(envDevkeyRegistry)),

		DeviceKeyAudience:    strings.TrimSpace(get(envDeviceKeyAudience)),
		DeviceKeyExecCommand: strings.TrimSpace(get(envDeviceKeyExecCommand)),
		DeviceKeyExecEnvName: strings.TrimSpace(get(envDeviceKeyExecEnvName)),

		DirectoryCacheTTL: DefaultDirectoryCacheTTL,
		ShutdownTimeout:   DefaultShutdownTimeout,
		LogLevel:          slog.LevelInfo,
	}

	for _, req := range []struct {
		name  string
		value string
	}{
		{envAddr, cfg.Addr},
		{envKCPHost, cfg.KCPHost},
		{envKCPKubeconfig, cfg.KCPKubeconfig},
		{envPLCURL, cfg.PLCURL},
		{envMintKubeconfigs, cfg.MintKubeconfigs},
		{envDevkeyRegistry, cfg.DevkeyRegistry},
		{envDeviceKeyAudience, cfg.DeviceKeyAudience},
		{envDeviceKeyExecCommand, cfg.DeviceKeyExecCommand},
		{envDeviceKeyExecEnvName, cfg.DeviceKeyExecEnvName},
	} {
		if req.value == "" {
			return Config{}, fmt.Errorf("%s is not set", req.name)
		}
	}

	if err := requireLoopbackAddr(cfg.Addr); err != nil {
		return Config{}, err
	}

	cfg.Audiences = splitList(get(envAudiences))
	if len(cfg.Audiences) == 0 {
		return Config{}, fmt.Errorf("%s is not set: the endpoint would refuse every token as Unauthorized", envAudiences)
	}

	var err error
	if cfg.DeviceKeyTTL, err = requiredDuration(get, envDeviceKeyTTL); err != nil {
		return Config{}, err
	}
	if cfg.MintTTL, err = optionalDuration(get, envMintTTL); err != nil {
		return Config{}, err
	}
	if cfg.MintFailureHold, err = optionalDuration(get, envMintFailureHold); err != nil {
		return Config{}, err
	}
	if cfg.DirectoryCacheTTL, err = duration(get, envDirectoryTTL, DefaultDirectoryCacheTTL); err != nil {
		return Config{}, err
	}
	if cfg.ShutdownTimeout, err = duration(get, envShutdownTimeout, DefaultShutdownTimeout); err != nil {
		return Config{}, err
	}
	if cfg.LogLevel, err = level(get(envLogLevel)); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// ponytail: loopback-only trusts a local proxy to terminate TLS; serve ListenAndServeTLS with
// cert and key paths when this process must terminate TLS itself
func requireLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s is %q: it is not a host:port address, and this listener must bind "+
			"127.0.0.1 or [::1]: %w", envAddr, addr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("%s is %q: the host %q is not a loopback address. This listener carries "+
			"bearer service-auth tokens in and device credentials with cluster access out, so TLS "+
			"must be terminated by a local proxy: bind 127.0.0.1 or [::1] only, never an "+
			"all-interfaces address", envAddr, addr, host)
	}
	return nil
}

func splitList(raw string) []string {
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func duration(get func(string) string, name string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(get(name))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration", name, raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: %q must be positive", name, raw)
	}
	return d, nil
}

func requiredDuration(get func(string) string, name string) (time.Duration, error) {
	if strings.TrimSpace(get(name)) == "" {
		return 0, fmt.Errorf("%s is not set: how long a device credential lives is a policy "+
			"decision, not a default, so this gateway will not choose one", name)
	}
	return duration(get, name, 0)
}

func optionalDuration(get func(string) string, name string) (time.Duration, error) {
	raw := strings.TrimSpace(get(name))
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a duration", name, raw)
	}
	if d <= 0 {
		return 0, fmt.Errorf("%s: %q must be positive", name, raw)
	}
	return d, nil
}

func level(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("%s: %q is not one of debug, info, warn, error", envLogLevel, raw)
	}
}
