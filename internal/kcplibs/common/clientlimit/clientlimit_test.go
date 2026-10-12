package clientlimit

import (
	"testing"

	"k8s.io/client-go/rest"
)

func TestApplyRaisesTheClientRateLimit(t *testing.T) {
	configured := Apply(&rest.Config{}, 0, 0)
	if configured.QPS != DefaultQPS || configured.Burst != DefaultBurst {
		t.Fatalf("tuned = (%v, %d), want (%v, %d)", configured.QPS, configured.Burst, DefaultQPS, DefaultBurst)
	}
	if DefaultQPS <= 5 {
		t.Fatal("client-go defaults every client to 5 qps with a burst of 10, which starves a controller")
	}
}

func TestApplyKeepsWhatTheCallerSet(t *testing.T) {
	source := &rest.Config{QPS: 7, Burst: 9}
	tuned := Apply(source, 0, 0)
	if tuned.QPS != 7 || tuned.Burst != 9 {
		t.Fatalf("tuned = (%v, %d), want the caller's (7, 9)", tuned.QPS, tuned.Burst)
	}
	if source.QPS != 7 {
		t.Fatal("Apply must not mutate the config it was given")
	}
	overridden := Apply(&rest.Config{}, 11, 12)
	if overridden.QPS != 11 || overridden.Burst != 12 {
		t.Fatalf("tuned = (%v, %d), want the options (11, 12)", overridden.QPS, overridden.Burst)
	}
}
