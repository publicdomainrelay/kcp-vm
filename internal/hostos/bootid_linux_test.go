//go:build linux

package hostos

import (
	"regexp"
	"testing"
)

var bootIDShape = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestBootIDIsTheKernelsUUIDAndStableWithinABoot(t *testing.T) {
	first, err := BootID()
	if err != nil {
		t.Fatal(err)
	}
	if !bootIDShape.MatchString(first) {
		t.Fatalf("BootID() = %q, not the kernel's UUID form", first)
	}
	second, err := BootID()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("BootID() changed within one boot: %q then %q", first, second)
	}
}
