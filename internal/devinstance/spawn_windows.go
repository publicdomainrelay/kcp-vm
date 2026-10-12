//go:build windows

package devinstance

import "errors"

func SpawnDetached(argv []string, logPath string) (int, error) {
	return 0, errors.New("devinstance: a detached devsup is unsupported on windows; see plan 0011")
}
