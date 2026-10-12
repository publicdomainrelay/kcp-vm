//go:build linux

package livekcp_test

import (
	"context"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/internal/livekcp"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/internal/livekcp/livetest"
)

func TestLiveStartupBreakdown(t *testing.T) {
	livetest.Require(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	started := time.Now()
	cluster, err := livekcp.Start(ctx)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer cluster.Stop()
	elapsed := time.Since(started)
	t.Logf("\n%s%8.2fs  Start() wall clock\n", cluster.Trace(), elapsed.Seconds())

	phases := cluster.Phases()
	if len(phases) < 4 {
		t.Fatalf("the breakdown lost its phases: %+v", phases)
	}
	var total time.Duration
	for _, phase := range phases {
		if phase.Name == "" || phase.Duration <= 0 {
			t.Fatalf("a phase must be named and take time: %+v", phase)
		}
		total += phase.Duration
	}
	if total > elapsed {
		t.Fatalf("the phases total %v, more than the %v the start took", total, elapsed)
	}
}
