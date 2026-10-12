//go:build linux

package livetest

import (
	"os"
	"strings"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/internal/livekcp"
)

func Require(t *testing.T) {
	t.Helper()
	missing := livekcp.Missing()
	if os.Getenv(livekcp.EnvRequire) == "1" {
		if len(missing) > 0 {
			t.Fatalf("live tests were required but %s are not on PATH", strings.Join(missing, ", "))
		}
		return
	}
	if len(missing) > 0 {
		t.Skipf("skipping the live tier: %s are not on PATH", strings.Join(missing, ", "))
	}
	t.Skipf("skipping the live tier: set %s=1 to run it", livekcp.EnvRequire)
}
