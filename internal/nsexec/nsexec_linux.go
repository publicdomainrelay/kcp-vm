//go:build linux

package nsexec

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/unix"
)

func Enter(c Config, self []string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	unshare, err := exec.LookPath(Unshare)
	if err != nil {
		return fmt.Errorf("nsexec: a private hostname needs util-linux %s on this host: %w", Unshare, err)
	}
	inner := append(append([]string(nil), self...), "--inside")
	argv := append([]string{unshare, "--user", "--map-current-user", "--keep-caps", "--uts", "--mount",
		"--propagation", "private", "--"}, Wrap(inner, c.Hostname, c.Binds, c.Argv)...)
	return syscall.Exec(unshare, argv, os.Environ())
}

func Inside(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := unix.Sethostname([]byte(c.Hostname)); err != nil {
		return fmt.Errorf("nsexec: sethostname %q: %w", c.Hostname, err)
	}
	for _, b := range c.Binds {
		st, err := os.Stat(b.Target)
		if err != nil || !st.IsDir() {
			continue
		}
		if err := os.MkdirAll(b.Source, 0o700); err != nil {
			return err
		}
		if err := unix.Mount(b.Source, b.Target, "", unix.MS_BIND|unix.MS_REC, ""); err != nil {
			return fmt.Errorf("nsexec: bind %s over %s: %w", b.Source, b.Target, err)
		}
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return fmt.Errorf("nsexec: clearing the ambient capabilities before exec: %w", err)
	}
	exe, err := exec.LookPath(c.Argv[0])
	if err != nil {
		return err
	}
	return syscall.Exec(exe, c.Argv, os.Environ())
}
