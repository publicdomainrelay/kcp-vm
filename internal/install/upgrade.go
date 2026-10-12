package install

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/bootstrap/table"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostcontract"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

var (
	ErrManifestMismatch = errors.New("upgrade: the binary does not match the sha256 it was published with")
	ErrSelfCheck        = errors.New("upgrade: the new binary's self-check failed")
	ErrNoPrevious       = errors.New("rollback: bin/previous names no binary")
	digestName          = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type UpgradeOptions struct {
	StateDir   string
	Home       string
	RuntimeDir string
	Proc       string
	Binary     string
	SHA256     string
	Out        io.Writer
	SelfCheck  func(ctx context.Context, exe string, role hostcontract.Role, stateDir string) error
	Restart    func(ctx context.Context, units []string) error
}

func (o UpgradeOptions) out() io.Writer {
	if o.Out == nil {
		return io.Discard
	}
	return o.Out
}

func (o UpgradeOptions) bin() string { return filepath.Join(o.StateDir, "bin") }

func isGatewayCore(name string) bool {
	return strings.HasPrefix(name, RuntimePrefix) && strings.HasSuffix(name, "-"+table.GatewayCore)
}

func isBidderCore(name string) bool {
	return strings.HasPrefix(name, RuntimePrefix) && strings.Contains(name, "-"+table.BidderCore) && strings.HasSuffix(name, hostsvc.ServiceSuffix)
}

func InstalledRole(home string) hostcontract.Role {
	entries, _ := os.ReadDir(filepath.Join(home, UserUnitDir))
	for _, e := range entries {
		if isGatewayCore(e.Name()) {
			return hostcontract.RoleGateway
		}
	}
	return hostcontract.RoleBidder
}

func EnabledCoreUnits(home string) []string {
	entries, _ := os.ReadDir(filepath.Join(home, UserUnitDir, WantsDir))
	var out []string
	for _, e := range entries {
		if isGatewayCore(e.Name()) || isBidderCore(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func readTarget(link string) string {
	t, err := os.Readlink(link)
	if err != nil || !digestName.MatchString(t) {
		return ""
	}
	return t
}

func Upgrade(ctx context.Context, o UpgradeOptions) error {
	if !digestName.MatchString(o.SHA256) {
		return fmt.Errorf("upgrade: --sha256 must name the published 64-hex digest of the binary, got %q", o.SHA256)
	}
	sum, err := fileSHA256(o.Binary)
	if err != nil {
		return err
	}
	if sum != o.SHA256 {
		return fmt.Errorf("%w: %s is %s, published %s", ErrManifestMismatch, o.Binary, sum, o.SHA256)
	}
	current := readTarget(filepath.Join(o.bin(), "current"))
	if current == sum {
		fmt.Fprintf(o.out(), "upgrade: bin/current is already %s\n", sum)
		return nil
	}
	dir := filepath.Join(o.bin(), sum)
	dst := filepath.Join(dir, CoreName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if have, _ := fileSHA256(dst); have != sum {
		if err := copyFile(o.Binary, dst, 0o755); err != nil {
			return err
		}
	}
	if have, err := fileSHA256(dst); err != nil || have != sum {
		return fmt.Errorf("%w: the copy at %s is %s", ErrManifestMismatch, dst, have)
	}
	role := InstalledRole(o.Home)
	if err := o.SelfCheck(ctx, dst, role, o.StateDir); err != nil {
		return fmt.Errorf("%w: %s: %v; bin/current still names %s", ErrSelfCheck, dst, err, current)
	}
	if current != "" {
		if err := retarget(filepath.Join(o.bin(), "previous"), current); err != nil {
			return err
		}
	}
	if err := retarget(filepath.Join(o.bin(), "current"), sum); err != nil {
		return err
	}
	fmt.Fprintf(o.out(), "upgrade: bin/current -> %s (previous %s)\n", sum, current)
	if err := o.restart(ctx); err != nil {
		return err
	}
	o.collect()
	return nil
}

func (o UpgradeOptions) restart(ctx context.Context) error {
	units := EnabledCoreUnits(o.Home)
	if len(units) == 0 {
		return nil
	}
	fmt.Fprintf(o.out(), "restarting %s\n", strings.Join(units, " "))
	return o.Restart(ctx, units)
}

func Rollback(ctx context.Context, o UpgradeOptions) error {
	previous := readTarget(filepath.Join(o.bin(), "previous"))
	if previous == "" {
		return ErrNoPrevious
	}
	if _, err := os.Stat(filepath.Join(o.bin(), previous, CoreName)); err != nil {
		return fmt.Errorf("%w: %v", ErrNoPrevious, err)
	}
	current := readTarget(filepath.Join(o.bin(), "current"))
	if err := retarget(filepath.Join(o.bin(), "current"), previous); err != nil {
		return err
	}
	if current != "" {
		if err := retarget(filepath.Join(o.bin(), "previous"), current); err != nil {
			return err
		}
	}
	fmt.Fprintf(o.out(), "rollback: bin/current -> %s (previous %s)\n", previous, current)
	if err := o.restart(ctx); err != nil {
		return err
	}
	o.collect()
	return nil
}
