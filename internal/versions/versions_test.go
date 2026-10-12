package versions

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
