//go:build !linux

package nodevm

import "errors"

var errNoACL = errors.New("nodevm: per-VM uid grants need POSIX ACLs, which only the linux build sets")

func GrantUser(path string, uid int, perm uint16) error { return errNoACL }

func GrantVMMFiles(workDir string, uid int) error { return errNoACL }
