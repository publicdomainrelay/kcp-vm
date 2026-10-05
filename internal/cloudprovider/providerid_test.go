package cloudprovider

import (
	"errors"
	"strings"
	"testing"
)

func TestProviderIDRoundTrips(t *testing.T) {
	cases := []struct {
		name    string
		cluster string
		node    string
	}{
		{"plain", "tenant-a", "node1"},
		{"hyphens and digits", "tenant-7", "worker-12"},
		{"long", "cluster-with-a-long-name", "node-with-a-long-name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ProviderID(tc.cluster, tc.node)
			want := "socialweb://" + tc.cluster + "/" + tc.node
			if got != want {
				t.Fatalf("ProviderID(%q, %q) = %q, want %q", tc.cluster, tc.node, got, want)
			}
			cluster, node, err := ParseProviderID(got)
			if err != nil {
				t.Fatalf("ParseProviderID(%q): %v", got, err)
			}
			if cluster != tc.cluster || node != tc.node {
				t.Fatalf("ParseProviderID(%q) = (%q, %q), want (%q, %q)",
					got, cluster, node, tc.cluster, tc.node)
			}
		})
	}
}

func TestParseProviderIDRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"wrong scheme", "kubernetes://cluster/node"},
		{"no scheme", "cluster/node"},
		{"scheme only", "socialweb://"},
		{"no slash", "socialweb://cluster"},
		{"empty cluster", "socialweb:///node1"},
		{"empty node", "socialweb://cluster/"},
		{"node holds a slash", "socialweb://cluster/node/extra"},
		{"non-ascii cluster byte", "socialweb://clust\xc3\xa9r/node1"},
		{"non-ascii node byte", "socialweb://cluster/n\xc3\xb8de1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ParseProviderID(tc.in)
			if err == nil {
				t.Fatalf("ParseProviderID(%q) succeeded, want a refusal", tc.in)
			}
			if !errors.Is(err, ErrBadProviderID) {
				t.Fatalf("ParseProviderID(%q) = %v, want it to wrap ErrBadProviderID", tc.in, err)
			}
			if !strings.Contains(err.Error(), "socialweb://") {
				t.Fatalf("ParseProviderID(%q) = %v, want the error to name the form", tc.in, err)
			}
		})
	}
}
