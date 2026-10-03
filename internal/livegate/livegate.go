package livegate

import (
	"fmt"
	"os"
	"testing"
)

// ponytail: two input classes and three modes. RequireLocal names an input the
// gate assumes this host carries (kcp, kine, k3s, postgres) and fails without
// it. Require names an artifact no host is assumed to carry (a built VMM and
// node image) and fails under --full, skips under --local, so a skip is always
// visible and never a pass. Drop the split if every host grows the artifacts.

func full() bool {
	return os.Getenv("SOCIALWEB_REQUIRE_LIVE") == "1" || os.Getenv("SOCIALWEB_LIVE") == "1"
}

func local() bool {
	return full() || os.Getenv("SOCIALWEB_LOCAL") == "1"
}

func RequiresLive() bool {
	return full()
}

func Require(t *testing.T, format string, args ...any) {
	t.Helper()
	if full() {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

func RequireLocal(t *testing.T, format string, args ...any) {
	t.Helper()
	if local() {
		t.Fatalf(format, args...)
	}
	t.Skipf(format, args...)
}

func Short(t *testing.T, format string, args ...any) {
	t.Helper()
	if !local() {
		t.Skipf(format, args...)
		return
	}
	if testing.Short() {
		t.Fatalf("the fast loop cannot be the full gate: %s", fmt.Sprintf(format, args...))
	}
}
