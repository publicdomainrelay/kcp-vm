package vaultmain

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostprocess"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/vaultlayout"
)

const (
	envStateDir             = "SOCIALWEB_STATE_DIR"
	envKCPKubeconfig        = "SOCIALWEB_KCP_KUBECONFIG"
	envKCPRootDir           = "SOCIALWEB_KCP_ROOT"
	envWorkspace            = "SOCIALWEB_ROOTVAULT_WORKSPACE"
	envServer               = "SOCIALWEB_ROOTVAULT_SERVER"
	envDeviceKeyAudience    = "SOCIALWEB_GATEWAY_DEVICE_KEY_AUDIENCE"
	envDevkeyRegistry       = "SOCIALWEB_GATEWAY_DEVKEY_REGISTRY"
	envDeviceKeyExecEnvName = "SOCIALWEB_GATEWAY_DEVICE_KEY_EXEC_ENV_NAME"
	envTLSCert              = "SOCIALWEB_TLS_CERT_FILE"
	envTLSKey               = "SOCIALWEB_TLS_KEY_FILE"
)

type Config struct {
	KCPKubeconfig string
	KCPRootDir    string
	Workspace     string
	Server        string

	DeviceKeyAudience    string
	DevkeyRegistry       string
	DeviceKeyExecEnvName string

	TLSCert string
	TLSKey  string
}

func load(get func(string) string) (Config, error) {
	cfg := Config{
		KCPKubeconfig:        strings.TrimSpace(get(envKCPKubeconfig)),
		KCPRootDir:           strings.TrimSpace(get(envKCPRootDir)),
		Workspace:            strings.TrimSpace(get(envWorkspace)),
		Server:               strings.TrimSpace(get(envServer)),
		DeviceKeyAudience:    strings.TrimSpace(get(envDeviceKeyAudience)),
		DevkeyRegistry:       strings.TrimSpace(get(envDevkeyRegistry)),
		DeviceKeyExecEnvName: strings.TrimSpace(get(envDeviceKeyExecEnvName)),
		TLSCert:              strings.TrimSpace(get(envTLSCert)),
		TLSKey:               strings.TrimSpace(get(envTLSKey)),
	}
	if cfg.KCPKubeconfig == "" {
		state := strings.TrimSpace(get(envStateDir))
		if state == "" {
			return Config{}, fmt.Errorf("neither %s nor %s is set: the root vault is an OpenBaoServer in kcp, read through the kcp front", envKCPKubeconfig, envStateDir)
		}
		cfg.KCPKubeconfig = filepath.Join(state, "kcp", "admin.kubeconfig")
	}
	if cfg.Workspace == "" {
		cfg.Workspace = hostprocess.DefaultWorkspace
	}
	if cfg.Server == "" {
		cfg.Server = vaultlayout.RootServerName
	}
	return cfg, nil
}
