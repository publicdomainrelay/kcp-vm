package kubeconfig

import (
	"errors"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const testDeviceCredential = "header.payload.signature"

func base() Input {
	return Input{
		ClusterName:      "web",
		Server:           "https://kcp.invalid/clusters/root:abc:web:child",
		ExecCommand:      "/usr/local/bin/socialweb-credential",
		DeviceCredential: testDeviceCredential,
		DeviceKeyEnvName: "SOCIALWEB_DEVICE_KEY",
	}
}

func TestAnHTTPServerIsRequired(t *testing.T) {
	in := base()
	in.Server = "http://kcp.invalid/clusters/root:abc:web:child"

	_, err := Build(in)
	if err == nil {
		t.Fatal("an http:// server was accepted -- client-go will SILENTLY discard the " +
			"exec plugin, the bearer token and every other credential, and it looks " +
			"identical to success until a request is made")
	}
	if !errors.Is(err, ErrNotHTTPS) {
		t.Fatalf("want ErrNotHTTPS, got %v", err)
	}
}

func TestInteractiveModeIsNever(t *testing.T) {
	out, err := Build(base())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	users, _ := doc["users"].([]any)
	if len(users) != 1 {
		t.Fatalf("want one user, got %d", len(users))
	}
	u, _ := users[0].(map[string]any)
	user, _ := u["user"].(map[string]any)
	exec, ok := user["exec"].(map[string]any)
	if !ok {
		t.Fatalf("no exec block -- a kubeconfig with no credential helper cannot authenticate: %v", user)
	}
	if exec["interactiveMode"] != "Never" {
		t.Fatalf("interactiveMode = %v, want Never: Always fails closed with ZERO plugin "+
			"invocations in a non-TTY process", exec["interactiveMode"])
	}
	if exec["apiVersion"] != ExecAPIVersion {
		t.Fatalf("apiVersion = %v, want %s", exec["apiVersion"], ExecAPIVersion)
	}
}

func TestThereIsNoBearerToken(t *testing.T) {
	out, err := Build(base())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	lower := strings.ToLower(out)
	for _, bad := range []string{"bearer_token", "token:", "client-certificate-data", "client-key-data"} {
		if strings.Contains(lower, bad) {
			t.Fatalf("the rendered kubeconfig contains %q -- see the must-not list. %s", bad, out)
		}
	}
	if err := AssertClean(out); err != nil {
		t.Fatalf("AssertClean: %v", err)
	}
}

func TestTheDeviceCredentialRidesTheEnvironment(t *testing.T) {
	out, err := Build(base())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out, "SOCIALWEB_DEVICE_KEY") {
		t.Fatal("the device credential's environment variable is absent, so the helper " +
			"has nothing to authenticate with")
	}
	if !strings.Contains(out, testDeviceCredential) {
		t.Fatal("the device credential's value did not reach the exec env -- it is the ONLY " +
			"thing the mint resolves the caller by, so a kubeconfig missing it produces a " +
			"helper that fails at the endpoint rather than a kubeconfig that fails here")
	}
}

func TestTheServerIsCarriedVerbatim(t *testing.T) {
	in := base()
	out, err := Build(in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out, "/clusters/root:abc:web:child") {
		t.Fatalf("the mount path was not carried verbatim:\n%s", out)
	}
}

func TestAnExecHelperIsRequired(t *testing.T) {
	in := base()
	in.ExecCommand = ""
	if _, err := Build(in); !errors.Is(err, ErrNoExec) {
		t.Fatalf("want ErrNoExec, got %v", err)
	}
}

func TestAssertCleanCatchesAPlantedLeak(t *testing.T) {
	clean, err := Build(base())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := AssertClean(clean); err != nil {
		t.Fatalf("a clean document was rejected: %v", err)
	}
	leaked := clean + "\n    token: sa-token-that-should-not-be-here\n"
	if err := AssertClean(leaked); err == nil {
		t.Fatal("AssertClean passed a document carrying a token -- the guard cannot fail, " +
			"so it is not a guard")
	}
}
