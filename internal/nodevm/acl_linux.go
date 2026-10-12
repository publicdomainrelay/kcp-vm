//go:build linux

package nodevm

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	aclVersion  = 2
	aclUserObj  = 0x01
	aclUser     = 0x02
	aclGroupObj = 0x04
	aclMask     = 0x10
	aclOther    = 0x20
	aclNoID     = 0xffffffff
)

func GrantUser(path string, uid int, perm uint16) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	mode := uint16(info.Mode().Perm())
	owner, group, other := (mode>>6)&7, (mode>>3)&7, mode&7
	entries := [][3]uint32{
		{aclUserObj, uint32(owner), aclNoID},
		{aclUser, uint32(perm & 7), uint32(uid)},
		{aclGroupObj, uint32(group), aclNoID},
		{aclMask, uint32((perm | group) & 7), aclNoID},
		{aclOther, uint32(other), aclNoID},
	}
	buf := make([]byte, 4+8*len(entries))
	binary.LittleEndian.PutUint32(buf, aclVersion)
	for i, e := range entries {
		off := 4 + 8*i
		binary.LittleEndian.PutUint16(buf[off:], uint16(e[0]))
		binary.LittleEndian.PutUint16(buf[off+2:], uint16(e[1]))
		binary.LittleEndian.PutUint32(buf[off+4:], e[2])
	}
	if err := unix.Setxattr(path, "system.posix_acl_access", buf, 0); err != nil {
		return fmt.Errorf("nodevm: granting uid %d %o on %s: %w", uid, perm, path, err)
	}
	return nil
}

func GrantVMMFiles(workDir string, uid int) error {
	entries, err := os.ReadDir(workDir)
	if err != nil {
		return err
	}
	self := os.Getuid()
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		path := filepath.Join(workDir, e.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != self {
			continue
		}
		if err := GrantUser(path, uid, 6); err != nil {
			return err
		}
	}
	return nil
}
