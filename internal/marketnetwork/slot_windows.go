//go:build windows

package marketnetwork

import (
	"errors"
	"os"
)

var errSlotUnsupported = errors.New("market network slots are unsupported on windows; see plan 0011")

func tryLock(f *os.File) (bool, error) {
	return false, errSlotUnsupported
}
