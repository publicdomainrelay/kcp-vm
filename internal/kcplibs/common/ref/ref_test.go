package ref

import "testing"

func TestKeyMatchesRefKey(t *testing.T) {
	r := New("root:alice", "default", "run-1")
	if r.Key() != Key("root:alice", "default", "run-1") {
		t.Fatalf("key = %q", r.Key())
	}
	if r.Key() != "root:alice/default/run-1" {
		t.Fatalf("key = %q", r.Key())
	}
}

func TestWithResourceVersion(t *testing.T) {
	r := New("root:alice", "default", "run-1").WithResourceVersion("42")
	if r.ResourceVersion != "42" || r.Name != "run-1" {
		t.Fatalf("ref = %+v", r)
	}
}

func TestBaseHost(t *testing.T) {
	cases := map[string]string{
		"https://kcp.example:6443":                          "https://kcp.example:6443",
		"https://kcp.example:6443/":                         "https://kcp.example:6443",
		"https://kcp.example:6443/clusters/root:alice":      "https://kcp.example:6443",
		"https://kcp.example:6443/clusters/root:alice/apis": "https://kcp.example:6443",
	}
	for in, want := range cases {
		if got := BaseHost(in); got != want {
			t.Fatalf("BaseHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClusterURL(t *testing.T) {
	if got := ClusterURL("https://kcp.example:6443/clusters/root:x", "root:alice"); got != "https://kcp.example:6443/clusters/root:alice" {
		t.Fatalf("ClusterURL = %q", got)
	}
}
