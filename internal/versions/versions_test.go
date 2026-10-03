package versions

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

func TestKcpMustRunTheWorkspaceMountsGate(t *testing.T) {
	kcp, err := Get("kcp")
	if err != nil {
		t.Fatalf("Get(kcp): %v", err)
	}
	if !strings.Contains(kcp.Invariant, "--feature-gates=WorkspaceMounts=true") {
		t.Fatal("the kcp entry must pin --feature-gates=WorkspaceMounts=true: it is ALPHA " +
			"and DEFAULT-OFF, and with it off every mount silently stops being served")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func functionalLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		out = append(out, l)
	}
	return out
}

func TestBothAuditPoliciesAreMetadataOnly(t *testing.T) {
	for _, path := range []string{
		"../../deploy/audit-policy.yaml",
		"../../deploy/kcp-audit-policy.yaml",
	} {
		t.Run(path, func(t *testing.T) {
			rules := functionalLines(mustRead(t, path))
			if len(rules) == 0 {
				t.Fatal("no functional lines; every assertion below would pass vacuously")
			}
			joined := strings.Join(rules, "\n")
			if !strings.Contains(joined, "level: Metadata") {
				t.Fatalf("%s does not set `level: Metadata`.\n\nADR 0031's line is WHO "+
					"AUTHENTICATED, NEVER WHAT RAN: Metadata is the lowest level carrying "+
					"userInfo, which is the entire payload. Request/RequestResponse add "+
					"bodies, which is the one thing this trail must not become.", path)
			}
			if strings.Contains(joined, "RequestResponse") || strings.Contains(joined, "level: Request") {
				t.Fatalf("%s captures request bodies. On kcp that writes EVERY TENANT'S "+
					"OBJECTS into a host-side file that dies with the host (D14).", path)
			}
			if !strings.Contains(joined, "- RequestReceived") {
				t.Fatalf("%s does not omit the RequestReceived stage, which fires before "+
					"authentication resolves and therefore carries NO userInfo -- the one "+
					"field both policies exist to capture.", path)
			}
		})
	}
}

func TestTheKcpAuditPolicyMatchesItsDeployedCopy(t *testing.T) {
	deployed := mustRead(t, "../../deploy/kcp-audit-policy.yaml")
	entrypoint := mustRead(t, "../../deploy/container-entrypoint.sh")

	named := auditPolicyInEntrypoint.FindStringSubmatch(entrypoint)
	if named == nil {
		t.Fatal("deploy/container-entrypoint.sh does not pass --audit-policy-file to `kcp start`: " +
			"the tracked policy is baked into the image and never read, so the shard audits NOTHING " +
			"while appearing armed")
	}
	if filepath.Base(named[1]) != filepath.Base("deploy/kcp-audit-policy.yaml") {
		t.Fatalf("the entrypoint reads the policy %q and the tracked deployed copy is "+
			"deploy/kcp-audit-policy.yaml. The image bakes the tracked file itself, so the two "+
			"cannot be different files without this drift", named[1])
	}
	rules := functionalLines(deployed)
	if len(rules) == 0 {
		t.Fatal("the tracked kcp audit policy has no functional lines; the comparison " +
			"below would pass vacuously")
	}
}

func TestKcpIsArmedWithItsAuditTrail(t *testing.T) {
	entrypoint := mustRead(t, "../../deploy/container-entrypoint.sh")
	for _, want := range []string{
		"--audit-policy-file=/opt/socialweb/deploy/kcp-audit-policy.yaml",
		"--audit-log-path=/var/lib/socialweb/kcp/audit.log",
	} {
		if !strings.Contains(entrypoint, want) {
			t.Fatalf("deploy/container-entrypoint.sh does not pass %s to `kcp start`: the policy "+
				"is written and never read, so the shard audits NOTHING while appearing armed", want)
		}
	}
	if !strings.Contains(entrypoint, "--audit-log-path=/var/lib/socialweb/kcp/") {
		t.Fatal("the kcp audit log path has moved off /var/lib/socialweb/kcp/")
	}
}

var (
	auditLogInScript        = regexp.MustCompile(`LOG=\$\{AUDIT_LOG:-([^}]+)\}`)
	auditLogInInstall       = regexp.MustCompile(`--audit-log-path=(\S+)`)
	auditPolicyInEntrypoint = regexp.MustCompile(`--audit-policy-file=(\S+)`)
)

func TestTheSARVerificationReadsTheLogTheDeploymentWrites(t *testing.T) {
	script := mustRead(t, "../../deploy/verify-sar-sweep.sh")
	install := mustRead(t, "../../deploy/container-entrypoint.sh")

	s := auditLogInScript.FindStringSubmatch(script)
	if s == nil {
		t.Fatal("verify-sar-sweep.sh no longer defaults its log path through " +
			"${AUDIT_LOG:-...}, so this check cannot tell what it reads")
	}
	l := auditLogInInstall.FindStringSubmatch(install)
	if l == nil {
		t.Fatal("deploy/container-entrypoint.sh no longer passes --audit-log-path, so kcp writes no audit " +
			"log and the verification's read can only ever hit its unreadable branch")
	}
	if s[1] != l[1] {
		t.Fatalf("verify-sar-sweep.sh reads %q and the deployment writes %q -- the check "+
			"would report an unreadable log, which is a FAILURE with the wrong cause",
			s[1], l[1])
	}
}

func jsonFieldNames(v any) []string {
	t := reflect.TypeOf(v)
	var out []string
	for i := 0; i < t.NumField(); i++ {
		name := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}

func schemaProperties(t *testing.T, ars map[string]any, path ...string) map[string]any {
	t.Helper()
	cur := ars
	for _, p := range path {
		m, ok := cur[p].(map[string]any)
		if !ok {
			t.Fatalf("the schema has no %q at %v", p, path)
		}
		cur = m
	}
	return cur
}

func TestTheAPISchemaCoversEveryFieldOfTheGoType(t *testing.T) {
	for _, api := range []struct {
		ars    string
		spec   any
		status any
	}{
		{
			ars:    "../../deploy/cluster-apiresourceschema.yaml",
			spec:   v1alpha1.ClusterSpec{},
			status: v1alpha1.ClusterStatus{},
		},
		{
			ars:    "../../deploy/atprotoidentity-apiresourceschema.yaml",
			spec:   v1alpha1.AtprotoIdentitySpec{},
			status: v1alpha1.AtprotoIdentityStatus{},
		},
	} {
		t.Run(filepath.Base(api.ars), func(t *testing.T) {
			var ars map[string]any
			if err := yaml.Unmarshal([]byte(mustRead(t, api.ars)), &ars); err != nil {
				t.Fatalf("parse the APIResourceSchema: %v", err)
			}

			var versions []any
			if v, ok := schemaProperties(t, ars, "spec")["versions"].([]any); ok {
				versions = v
			}
			if len(versions) == 0 {
				t.Fatal("the schema declares no versions")
			}
			v0, ok := versions[0].(map[string]any)
			if !ok {
				t.Fatal("versions[0] is not an object")
			}
			props, ok := v0["schema"].(map[string]any)["properties"].(map[string]any)
			if !ok {
				t.Fatal("versions[0].schema.properties is missing")
			}

			for _, tc := range []struct {
				obj  string
				typ  any
				drop string
			}{
				{
					obj: "spec", typ: api.spec,
					drop: "a spec field the schema omits is DROPPED, so the object a controller " +
						"reads is not the object a caller wrote",
				},
				{
					obj: "status", typ: api.status,
					drop: "THIS IS THE nodeExpiry CASE: the measured recipe omitted it, and " +
						"`storage: {crd: {}}` would have materialised a CRD that discarded every " +
						"node-expiry write -- silently, because the object still validates",
				},
			} {
				t.Run(tc.obj, func(t *testing.T) {
					declared, ok := props[tc.obj].(map[string]any)["properties"].(map[string]any)
					if !ok {
						t.Fatalf("the schema declares no properties for %q", tc.obj)
					}
					for _, want := range jsonFieldNames(tc.typ) {
						if _, ok := declared[want]; !ok {
							t.Errorf("api/v1alpha1 .%s carries %q and the APIResourceSchema does "+
								"not declare it.\n\n%s\n\nAdd it to %s, or remove it from the Go type.",
								tc.obj, want, tc.drop, api.ars)
						}
					}
				})
			}
		})
	}
}

func TestTheWorkspaceTypeBindsAnExportThatExists(t *testing.T) {
	for _, tc := range []struct{ workspaceType, export string }{
		{"../../deploy/workspacetype-cluster.yaml", "../../deploy/cluster-apiexport.yaml"},
		{"../../deploy/workspacetype-account.yaml", "../../deploy/atprotoidentity-apiexport.yaml"},
	} {
		t.Run(filepath.Base(tc.workspaceType), func(t *testing.T) {
			var wt map[string]any
			if err := yaml.Unmarshal([]byte(mustRead(t, tc.workspaceType)), &wt); err != nil {
				t.Fatalf("parse the workspace type: %v", err)
			}
			var export map[string]any
			if err := yaml.Unmarshal([]byte(mustRead(t, tc.export)), &export); err != nil {
				t.Fatalf("parse the APIExport: %v", err)
			}

			spec := schemaProperties(t, wt, "spec")
			bindings, ok := spec["defaultAPIBindings"].([]any)
			if !ok || len(bindings) == 0 {
				t.Fatal("this type declares NO defaultAPIBindings. That is the mechanism a workspace gets " +
					"its kinds through, so without it the workspace reaches Ready and the kind " +
					"never appears.")
			}

			exportName, _ := schemaProperties(t, export, "metadata")["name"].(string)
			if exportName == "" {
				t.Fatal("the APIExport declares no metadata.name")
			}
			for _, b := range bindings {
				m, ok := b.(map[string]any)
				if !ok {
					t.Fatalf("defaultAPIBindings entry is not an object: %v", b)
				}
				ref, ok := m["export"].(string)
				if !ok || ref == "" {
					t.Fatalf("defaultAPIBindings entry has no `export` string: %v", m)
				}
				if ref != exportName {
					t.Errorf("the type binds export %q and its APIExport declares %q -- "+
						"the workspace is created, reaches Ready, and NEVER HAS THE KIND, which "+
						"reads like a kcp fault rather than a typo", ref, exportName)
				}
				p, _ := m["path"].(string)
				if p == "" {
					t.Error("the export reference has no `path`; without it the binding resolves " +
						"in the SAME workspace, and the export lives in the provider's")
				}
			}

			ext, ok := spec["extend"].(map[string]any)["with"].([]any)
			if !ok || len(ext) == 0 {
				t.Fatal("the type does not EXTEND universal. A type that replaces rather than " +
					"extends starts from no permissions, so every workspace made with it is " +
					"useless in a way that looks like an RBAC bug.")
			}
		})
	}
}

func stubKubectl(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
case "$3 $4" in
  "create clusterroles.rbac.authorization.k8s.io" | \
  "create clusterrolebindings.rbac.authorization.k8s.io" | \
  "create rolebindings.rbac.authorization.k8s.io" | \
  "create certificatesigningrequests.certificates.k8s.io" | \
  "create pods" | \
  "get nodes" | \
  "update shards.core.kcp.io") echo no ;;
  *) echo yes ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0o755); err != nil {
		t.Fatalf("writing the stub kubectl: %v", err)
	}
	return dir
}

func runSweep(t *testing.T, logPath string) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", "../../deploy/verify-sar-sweep.sh")
	cmd.Env = append(os.Environ(),
		"PATH="+stubKubectl(t)+":"+os.Getenv("PATH"),
		"KUBECTL=kubectl", "TENANT_KUBECTL=kubectl",
		"AUDIT_LOG="+logPath,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return string(out), 0
	}
	var exited *exec.ExitError
	if !errors.As(err, &exited) {
		t.Fatalf("running the sweep: %v", err)
	}
	return string(out), exited.ExitCode()
}

func TestTheAuditAssertionFailsClosedOnAnUnarmedLog(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "definitely-not-there.log")
	out, code := runSweep(t, absent)
	if code == 0 {
		t.Fatalf("the sweep exited 0 with an audit log it could not read. ADR 0027: an unarmed "+
			"audit log is a FAILURE of this spec, not a skip -- the run is recorded as evidence "+
			"that shard-admin is unused while NOTHING WAS EVER READ.\n\noutput:\n%s", out)
	}
	if !strings.Contains(out, "SKIPPING audit assertion") {
		t.Fatalf("an unreadable audit log did not produce the skip message, so the check did not "+
			"notice it was unarmed.\n\noutput:\n%s", out)
	}
	if !strings.Contains(out, absent) {
		t.Errorf("the skip message does not name the log it could not read, so an operator "+
			"cannot tell which path was checked:\n%s", out)
	}

	clean := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(clean, []byte(`{"username":"kcp-admin","verb":"get"}`+"\n"), 0o600); err != nil {
		t.Fatalf("writing the clean log: %v", err)
	}
	out, code = runSweep(t, clean)
	if code != 0 {
		t.Fatalf("a READABLE, CLEAN audit log also exited %d. This is the control the assertion "+
			"above needs: the exit status only attributes the failure to the audit branch if a "+
			"run with nothing else wrong exits 0.\n\noutput:\n%s", code, out)
	}
	if strings.Contains(out, "SKIPPING audit assertion") {
		t.Fatalf("a READABLE log was still reported as skipped, so the check is unconditional and "+
			"the unarmed case above proves nothing.\n\noutput:\n%s", out)
	}
}

func TestTheAuditAssertionCatchesShardAdminInTheLog(t *testing.T) {
	dirty := filepath.Join(t.TempDir(), "audit.log")
	if err := os.WriteFile(dirty, []byte(`{"username":"shard-admin","verb":"get"}`+"\n"), 0o600); err != nil {
		t.Fatalf("writing the log: %v", err)
	}
	out, code := runSweep(t, dirty)
	if !strings.Contains(out, "SHARD-ADMIN IS STILL BEING USED") {
		t.Fatalf("a log carrying shard-admin was not reported -- the check that exists to prove "+
			"shard_admin_is_unused is not looking at the records it names.\n\noutput:\n%s", out)
	}
	if code == 0 {
		t.Fatalf("the sweep exited 0 with shard-admin in the log: the finding is printed and "+
			"not propagated, so nothing that runs this can act on it.\n\noutput:\n%s", out)
	}
}
