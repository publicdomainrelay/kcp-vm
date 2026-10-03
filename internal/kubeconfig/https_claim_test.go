package kubeconfig

import (
	"testing"

	"k8s.io/client-go/tools/clientcmd"
)

func TestTheHTTPClaimIsTrue(t *testing.T) {
	httpsDoc, err := Build(Input{
		ClusterName:      "web",
		Server:           "https://kcp.invalid/clusters/root:abc:web:child",
		ExecCommand:      "/usr/bin/true",
		DeviceCredential: "header.payload.signature",
		DeviceKeyEnvName: "SOCIALWEB_DEVICE_KEY",
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	const httpDoc = `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster: {server: "http://kcp.invalid/clusters/root:abc:web:child"}
users:
- name: u
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1
      command: /usr/bin/true
      interactiveMode: Never
contexts:
- name: x
  context: {cluster: c, user: u}
current-context: x
`

	for _, tc := range []struct {
		scheme   string
		doc      string
		wantExec bool
	}{
		{"https", httpsDoc, true},
		{"http", httpDoc, false},
	} {
		cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(tc.doc))
		if err != nil {
			t.Fatalf("%s: RESTConfigFromKubeConfig returned an ERROR: %v\n\n"+
				"The comment on ErrNotHTTPS says the drop happens with NO ERROR AT ANY "+
				"LAYER. If client-go now REPORTS it, the guard's reason has changed and the "+
				"comment is what needs updating -- not this test.", tc.scheme, err)
		}
		if got := cfg.ExecProvider != nil; got != tc.wantExec {
			t.Fatalf("%s: ExecProvider != nil = %v, want %v.\n\nErrNotHTTPS exists because "+
				"an http:// server makes client-go DROP THE ENTIRE USER-IDENTITY BLOCK with "+
				"no error -- exec, bearer token, client certificate and basic auth alike. "+
				"If that is no longer true, the guard can be re-evaluated; if it is, this "+
				"test is what keeps the reason attached to it.", tc.scheme, got, tc.wantExec)
		}
	}
}
