package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/rootvault"
)

const (
	envAddr                 = "SOCIALWEB_ROOTVAULT_ADDR"
	envStateDir             = "SOCIALWEB_STATE_DIR"
	envKCPKubeconfig        = "SOCIALWEB_KCP_KUBECONFIG"
	envDeviceKeyAudience    = "SOCIALWEB_GATEWAY_DEVICE_KEY_AUDIENCE"
	envDevkeyRegistry       = "SOCIALWEB_GATEWAY_DEVKEY_REGISTRY"
	envDeviceKeyExecEnvName = "SOCIALWEB_GATEWAY_DEVICE_KEY_EXEC_ENV_NAME"
	envContainerTLSCert     = "SOCIALWEB_CONTAINER_TLS_CERT"
	envContainerTLSKey      = "SOCIALWEB_CONTAINER_TLS_KEY"
)

const (
	defaultStateDir = "/var/lib/socialweb"
)

func unsealFileName() string { return "root-unseal.json" }

type Config struct {
	Addr     string
	StateDir string

	KCPKubeconfig string
	UnsealPath    string

	DeviceKeyAudience    string
	DevkeyRegistry       string
	DeviceKeyExecEnvName string

	ContainerTLSCert string
	ContainerTLSKey  string
}

func Load() (Config, error) { return load(os.Getenv) }

func load(get func(string) string) (Config, error) {
	stateDir := strings.TrimSpace(get(envStateDir))
	if stateDir == "" {
		stateDir = defaultStateDir
	}
	if !filepath.IsAbs(stateDir) {
		return Config{}, fmt.Errorf("%s is %q, not absolute: the unseal material and the vault's "+
			"own store are both derived from it, and a relative one resolves against whichever "+
			"directory the entrypoint started in, so the very file that unseals the vault lands "+
			"somewhere nobody looks", envStateDir, stateDir)
	}

	addr := strings.TrimSpace(get(envAddr))
	if addr == "" {
		addr = rootvault.Address()
	}
	if !strings.Contains(addr, "://") {
		addr = "https://" + addr
	}

	kubeconfig := strings.TrimSpace(get(envKCPKubeconfig))
	if kubeconfig == "" {
		kubeconfig = filepath.Join(stateDir, "kcp", "admin.kubeconfig")
	}

	return Config{
		Addr:     addr,
		StateDir: stateDir,
		UnsealPath: filepath.Join(
			rootvault.Dir(stateDir), unsealFileName()),

		KCPKubeconfig: kubeconfig,

		DeviceKeyAudience:    strings.TrimSpace(get(envDeviceKeyAudience)),
		DevkeyRegistry:       strings.TrimSpace(get(envDevkeyRegistry)),
		DeviceKeyExecEnvName: strings.TrimSpace(get(envDeviceKeyExecEnvName)),

		ContainerTLSCert: strings.TrimSpace(get(envContainerTLSCert)),
		ContainerTLSKey:  strings.TrimSpace(get(envContainerTLSKey)),
	}, nil
}
