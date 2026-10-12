package gatewaykcp

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/deploy"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostprocess"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
)

const (
	DefaultAudience         = "did:web:socialweb.local"
	DefaultPLC              = "https://plc.directory"
	DefaultDevkeyRegistry   = "root:socialweb-provider"
	DefaultDeviceKeyTTL     = "24h"
	DefaultDeviceKeyExec    = "socialweb-credential"
	DefaultDeviceKeyExecEnv = "SOCIALWEB_DEVICE_KEY"
	DefaultSubstrate        = "market"
	DefaultPortRange        = "20000-29999"
	DefaultJoin             = "*"
	DeployDir               = "deploy"
	tenantGrantFile         = "tenant-grant.yaml"
	auditPolicyFile         = "audit-policy.yaml"
	esoDir                  = "eso"
)

type env struct{ m map[string]string }

func newEnv(environ []string) *env {
	e := &env{m: map[string]string{}}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if ok {
			e.m[k] = v
		}
	}
	return e
}

func (e *env) get(k string) string { return e.m[k] }

func (e *env) set(k, v string) { e.m[k] = v }

func (e *env) dflt(k, v string) {
	if strings.TrimSpace(e.m[k]) == "" {
		e.m[k] = v
	}
}

func (e *env) list() []string {
	out := make([]string, 0, len(e.m))
	for k, v := range e.m {
		out = append(out, k+"="+v)
	}
	return out
}

func materialize(dir string) (string, string, string, error) {
	grant, audit, eso := filepath.Join(dir, tenantGrantFile), filepath.Join(dir, auditPolicyFile), filepath.Join(dir, esoDir)
	if err := os.MkdirAll(eso, 0o700); err != nil {
		return "", "", "", err
	}
	copyOut := func(src, dst string) error {
		b, err := fs.ReadFile(deploy.FS, src)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o600)
	}
	for src, dst := range map[string]string{tenantGrantFile: grant, auditPolicyFile: audit} {
		if err := copyOut(src, dst); err != nil {
			return "", "", "", err
		}
	}
	entries, err := fs.ReadDir(deploy.FS, esoDir)
	if err != nil {
		return "", "", "", err
	}
	for _, en := range entries {
		if err := copyOut(esoDir+"/"+en.Name(), filepath.Join(eso, en.Name())); err != nil {
			return "", "", "", err
		}
	}
	return grant, audit, eso, nil
}

func (g *gateway) defaults(e *env, rec devinstance.Record, cert, key string) error {
	state := g.c.StateDir
	mint := filepath.Join(state, "mint")
	os.MkdirAll(mint, 0o700)
	e.set("SOCIALWEB_KCP_HOST", rec.KcpURL)
	e.set("SOCIALWEB_KCP_KUBECONFIG", g.memberKubeconfig())
	e.set("SOCIALWEB_GATEWAY_MINT_KUBECONFIGS", mint)
	e.dflt("SOCIALWEB_GATEWAY_ADDR", DefaultAddress)
	e.dflt("SOCIALWEB_GATEWAY_AUDIENCES", DefaultAudience)
	first, _, _ := strings.Cut(e.get("SOCIALWEB_GATEWAY_AUDIENCES"), ",")
	e.dflt("SOCIALWEB_GATEWAY_DEVICE_KEY_AUDIENCE", strings.TrimSpace(first))
	e.dflt("SOCIALWEB_GATEWAY_PLC_URL", DefaultPLC)
	e.dflt("SOCIALWEB_GATEWAY_DEVKEY_REGISTRY", DefaultDevkeyRegistry)
	e.dflt("SOCIALWEB_GATEWAY_DEVICE_KEY_TTL", DefaultDeviceKeyTTL)
	e.dflt("SOCIALWEB_GATEWAY_DEVICE_KEY_EXEC_COMMAND", DefaultDeviceKeyExec)
	e.dflt("SOCIALWEB_GATEWAY_DEVICE_KEY_EXEC_ENV_NAME", DefaultDeviceKeyExecEnv)
	e.set("SOCIALWEB_CLUSTERPROVIDER_UNIT_PREFIX", g.l.Names().HostProcessPrefix())
	e.set("SOCIALWEB_CLUSTERPROVIDER_HOSTPROCESS_WORKSPACE", hostprocess.DefaultWorkspace)
	e.set("SOCIALWEB_CLUSTERPROVIDER_MOUNT_CONTEXT", kcpclient.ShardAdminContext)
	e.dflt("SOCIALWEB_CLUSTERPROVIDER_STATE_DIR", filepath.Join(state, "clusterprovider"))
	e.dflt("SOCIALWEB_CLUSTERPROVIDER_LEASE_PATH", filepath.Join(state, "leases.json"))
	e.dflt("SOCIALWEB_CLUSTERPROVIDER_SUBSTRATE", DefaultSubstrate)
	e.dflt("SOCIALWEB_CLUSTERPROVIDER_PORT_RANGE", DefaultPortRange)
	e.dflt("SOCIALWEB_CLUSTERPROVIDER_JOIN", DefaultJoin)
	e.dflt("SOCIALWEB_CLUSTERPROVIDER_DATASTORE_HAS_ROW", "false")
	for env, name := range map[string]string{"SOCIALWEB_CLUSTERPROVIDER_EXTERNAL_SECRETS": ExeExternalSecrets,
		"SOCIALWEB_CLUSTERPROVIDER_ESO_BUNDLE": ExeESOBundle, "SOCIALWEB_CLUSTERPROVIDER_DUMBPIPE": ExeDumbpipe} {
		if p := g.c.Executables[name].Path; p != "" {
			e.dflt(env, p)
		}
	}
	if h, err := os.Hostname(); err == nil {
		e.dflt("SOCIALWEB_CLUSTERPROVIDER_HOST", h)
	}
	os.MkdirAll(e.get("SOCIALWEB_CLUSTERPROVIDER_STATE_DIR"), 0o700)
	if grant, audit, eso, err := materialize(filepath.Join(state, DeployDir)); err == nil {
		e.dflt("SOCIALWEB_CLUSTERPROVIDER_GRANT", grant)
		e.dflt("SOCIALWEB_CLUSTERPROVIDER_AUDIT_POLICY", audit)
		e.dflt("SOCIALWEB_CLUSTERPROVIDER_ESO_MANIFEST_DIR", eso)
	} else {
		g.c.log().Warn("the embedded deploy files could not be written; the clusterprovider reads its own defaults", "err", err.Error())
	}
	e.set("SOCIALWEB_TLS_CERT_FILE", cert)
	e.set("SOCIALWEB_TLS_KEY_FILE", key)
	return nil
}

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }
