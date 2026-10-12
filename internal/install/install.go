package install

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/bootstrap/table"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostcontract"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
)

//go:embed templates
var templates embed.FS

const (
	AccountGateway  = "socialweb"
	AccountBidder   = "swc-bidder"
	StateRoot       = "/var/lib/socialweb-computer"
	DefaultVMSlots  = 256
	SubidFloor      = 100000
	KVMGroup        = "kvm"
	CoreName        = "socialweb-computer"
	GatewayUnit     = hostsvc.NamePrefix + table.GatewayCore
	BidderTemplate  = hostsvc.NamePrefix + table.BidderCore + hostsvc.ServiceSuffix
	SliceUnit       = "socialweb.slice"
	DropInName      = "socialweb-delegate.conf"
	AppArmorPath    = "/etc/apparmor.d/" + hostcontract.AppArmorProfileName
	NodePassword    = "/etc/rancher/node/password"
	LingerDir       = "/var/lib/systemd/linger"
	RestrictUserns  = "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"
	AppArmorLoaded  = "/sys/kernel/security/apparmor/profiles"
	ConfigPathEnv   = "CONFIG_PATH_SOCIALWEB_COMPUTER"
	UserUnitDir     = ".config/systemd/user"
	WantsDir        = "default.target.wants"
	RuntimePrefix   = hostsvc.NamePrefix
	RuntimeSliceTop = "socialweb"
)

var (
	ErrProcBackend       = errors.New("install: install places production units on the systemd-user backend only; host-backend proc is the dev run mode (socialweb-computer dev up)")
	ErrForeignAccount    = errors.New("install: refusing to take over an existing account no install record says install created")
	ErrManagerWithoutKVM = errors.New("install: the account's user manager already runs without the kvm group")
	ErrVMsRunning        = errors.New("uninstall: VM units of the account are active; stopping one makes its VM Lost")
	ErrNoRecord          = errors.New("uninstall: no install record for this account")
	ErrUnsupported       = errors.New("install: install and uninstall are Linux root steps; this OS has no backend (plans 0011, 0012)")
	handleRE             = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
	accountRE            = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,30}$`)
	stateRE              = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)
)

type Account struct {
	Name string
	UID  int
	GID  int
	Home string
}

type UserManager interface {
	Reload(ctx context.Context) error
	Active(ctx context.Context, unit string) (bool, error)
	EnableNow(ctx context.Context, units []string) error
	DisableStop(ctx context.Context, units []string) error
	List(ctx context.Context) ([]hostsvc.UnitStatus, error)
	Stop(ctx context.Context, unit string) error
	Close()
}

type System interface {
	LookupAccount(name string) (Account, bool, error)
	CreateAccount(name, home string) error
	DeleteAccount(name string) error
	AddToGroup(account, group string) error
	RemoveFromGroup(account, group string) error
	Chown(path string, uid, gid int) error
	Owner(path string) (int, int, error)
	ReloadSystemManager(ctx context.Context) error
	LoadAppArmor(path string) error
	UnloadAppArmor(path string) error
	SetLinger(ctx context.Context, a Account, on bool) error
	User(ctx context.Context, a Account) (UserManager, error)
	CheckHost(ctx context.Context, a Account, binary string, role hostcontract.Role, stateDir string) error
}

type Options struct {
	Role         hostcontract.Role
	Handles      []string
	Account      string
	StateDir     string
	Binary       string
	VMSlots      int
	DryRun       bool
	AdoptAccount bool
	HostBackend  string
	Root         string
	Staged       bool
	UnitPrefix   string
	Settings     map[string]string
	Out          io.Writer
}

const (
	OptKcpFront = "kcp-front-address"
	OptTLSFront = "tls-front-address"
	OptSpecFile = "gateway-spec-file"
)

var GatewayListen = map[string]string{OptKcpFront: "127.0.0.1:6443", OptTLSFront: "127.0.0.1:8443"}

var Passed = []string{OptKcpFront, OptTLSFront, OptSpecFile}

func Rooted(root string) bool { return root != "" && filepath.Clean(root) != "/" }

func prefixOr(p string) string {
	if p == "" {
		return hostsvc.NamePrefix
	}
	return p
}

type StepResult struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail,omitempty"`
}

type Report struct {
	Steps   []StepResult `json:"steps"`
	Changes []string     `json:"changes"`
}

func DefaultAccount(role hostcontract.Role) string {
	if role == hostcontract.RoleBidder {
		return AccountBidder
	}
	return AccountGateway
}

func RecordPath(account string) string { return filepath.Join(StateRoot, "install-"+account+".json") }

func HandleID(handle string) string {
	s := sha256.Sum256([]byte(handle))
	return hex.EncodeToString(s[:])[:10]
}

func CoreUnits(role hostcontract.Role, prefix string, handles []string) []string {
	if role == hostcontract.RoleGateway {
		return []string{table.CoreTemplate(prefix, table.RoleGateway)}
	}
	out := make([]string, 0, len(handles))
	for _, h := range handles {
		out = append(out, prefixOr(prefix)+table.BidderCore+h+hostsvc.ServiceSuffix)
	}
	sort.Strings(out)
	return out
}

type installer struct {
	ctx     context.Context
	o       Options
	sys     System
	rep     *Report
	rec     *Record
	recPath string
	acct    Account
	known   bool
	unitsUp bool
	planned map[string]bool
}

func within(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+"/") }

func (in *installer) path(p string) string {
	if in.o.Staged && in.o.StateDir != "" && (within(p, in.o.StateDir) || within(in.o.StateDir, p)) {
		return p
	}
	return filepath.Join(in.o.Root, p)
}

func (in *installer) out() io.Writer {
	if in.o.Out == nil {
		return io.Discard
	}
	return in.o.Out
}

func (in *installer) change(desc string, f func() error) error {
	in.rep.Changes = append(in.rep.Changes, desc)
	if in.o.DryRun {
		fmt.Fprintf(in.out(), "would %s\n", desc)
		return nil
	}
	fmt.Fprintf(in.out(), "%s\n", desc)
	if err := f(); err != nil {
		return fmt.Errorf("install: %s: %w", desc, err)
	}
	return nil
}

func (in *installer) step(name, outcome, detail string) {
	in.rep.Steps = append(in.rep.Steps, StepResult{Name: name, Outcome: outcome, Detail: detail})
	if outcome == OutcomeNotNeeded {
		return
	}
	in.rec.set(name, outcome)
}

func (in *installer) saveRecord() error {
	if in.o.DryRun {
		return nil
	}
	b := in.rec.bytes()
	if sameBytes(in.recPath, b) {
		return in.ensureOwner(in.recPath, recordMode, 0, 0)
	}
	return in.change("write the install record "+RecordPath(in.rec.Account), func() error {
		if err := os.MkdirAll(filepath.Dir(in.recPath), 0o755); err != nil {
			return err
		}
		return writeAtomic(in.recPath, b, recordMode)
	})
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".swc-install-")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (in *installer) ensureOwner(full string, mode os.FileMode, uid, gid int) error {
	st, err := os.Lstat(full)
	if err != nil {
		return err
	}
	if st.Mode().Perm() != mode.Perm() && st.Mode()&fs.ModeSymlink == 0 {
		if err := in.change(fmt.Sprintf("chmod %o %s", mode.Perm(), strings.TrimPrefix(full, in.o.Root)), func() error { return os.Chmod(full, mode) }); err != nil {
			return err
		}
	}
	if uid < 0 {
		return nil
	}
	ou, og, err := in.sys.Owner(full)
	if err != nil {
		return err
	}
	if ou == uid && og == gid {
		return nil
	}
	return in.change(fmt.Sprintf("chown %d:%d %s", uid, gid, strings.TrimPrefix(full, in.o.Root)), func() error { return in.sys.Chown(full, uid, gid) })
}

func (in *installer) ensureDir(host string, mode os.FileMode, uid, gid int, track bool) error {
	full := in.path(host)
	if in.planned[host] {
		return nil
	}
	if _, err := os.Stat(full); errors.Is(err, fs.ErrNotExist) {
		parent := filepath.Dir(host)
		if parent != host {
			if _, err := os.Stat(in.path(parent)); errors.Is(err, fs.ErrNotExist) {
				if err := in.ensureDir(parent, 0o755, 0, 0, track); err != nil {
					return err
				}
			}
		}
		if err := in.change("create directory "+host, func() error { return os.Mkdir(full, mode) }); err != nil {
			return err
		}
		if track {
			in.rec.addDir(host)
		}
		if in.o.DryRun {
			if in.planned == nil {
				in.planned = map[string]bool{}
			}
			in.planned[host] = true
			return nil
		}
	}
	return in.ensureOwner(full, mode, uid, gid)
}

func (in *installer) ensureFile(host string, content []byte, mode os.FileMode, uid, gid int) (bool, error) {
	full := in.path(host)
	if sameBytes(full, content) {
		return false, in.ensureOwner(full, mode, uid, gid)
	}
	err := in.change("write "+host, func() error {
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		return writeAtomic(full, content, mode)
	})
	if err != nil || in.o.DryRun {
		return true, err
	}
	return true, in.ensureOwner(full, mode, uid, gid)
}

func (in *installer) read(host string) string {
	b, _ := os.ReadFile(in.path(host))
	return string(b)
}

func validate(o *Options) error {
	if o.HostBackend != "" && o.HostBackend != string(hostsvc.BackendSystemdUser) {
		if o.HostBackend == string(hostsvc.BackendProc) {
			return ErrProcBackend
		}
		return fmt.Errorf("install: host-backend %q: install writes %s", o.HostBackend, hostsvc.BackendSystemdUser)
	}
	if _, err := hostcontract.ParseRole(string(o.Role)); err != nil {
		return err
	}
	if o.Account == "" {
		o.Account = DefaultAccount(o.Role)
	}
	if !accountRE.MatchString(o.Account) {
		return fmt.Errorf("install: account name %q is not a valid system account name", o.Account)
	}
	if o.StateDir == "" {
		o.StateDir = filepath.Join(StateRoot, o.Account)
	}
	if !stateRE.MatchString(o.StateDir) || filepath.Clean(o.StateDir) != o.StateDir {
		return fmt.Errorf("install: state dir %q must be a clean absolute path of [A-Za-z0-9._/-] (it is written into unit files)", o.StateDir)
	}
	if o.VMSlots == 0 {
		o.VMSlots = DefaultVMSlots
	}
	if o.VMSlots < 1 {
		return fmt.Errorf("install: --vm-slots %d: want at least 1", o.VMSlots)
	}
	if o.Root == "" {
		o.Root = "/"
	}
	if o.UnitPrefix != "" {
		if err := hostsvc.ValidatePrefix(o.UnitPrefix); err != nil {
			return fmt.Errorf("install: unit-prefix: %w", err)
		}
		if !strings.HasSuffix(o.UnitPrefix, "-") {
			return fmt.Errorf("install: unit-prefix %q must end in -", o.UnitPrefix)
		}
	}
	switch o.Role {
	case hostcontract.RoleGateway:
		if len(o.Handles) > 0 {
			return fmt.Errorf("install gateway takes no --handle")
		}
	case hostcontract.RoleBidder:
		if len(o.Handles) == 0 {
			return fmt.Errorf("install bidder needs at least one --handle (the atproto handle each bidder instance runs as)")
		}
		for _, h := range o.Handles {
			if !handleRE.MatchString(h) {
				return fmt.Errorf("install: handle %q is not a lowercase atproto handle", h)
			}
		}
	}
	if o.Binary == "" || !filepath.IsAbs(o.Binary) {
		return fmt.Errorf("install: the binary to install must be an absolute path, got %q", o.Binary)
	}
	return nil
}

func Install(ctx context.Context, o Options, sys System) (Report, error) {
	rep := Report{}
	if err := validate(&o); err != nil {
		return rep, err
	}
	in := &installer{ctx: ctx, o: o, sys: sys, rep: &rep, recPath: filepath.Join(o.Root, RecordPath(o.Account))}
	rec, _, err := loadOrNewRecord(in.recPath, o.Role, o.Account, o.StateDir)
	if err != nil {
		return rep, err
	}
	if rec.Role != o.Role {
		return rep, fmt.Errorf("install: %s was installed as a %s; uninstall it first", o.Account, rec.Role)
	}
	if rec.StateDir != o.StateDir {
		return rep, fmt.Errorf("install: %s was installed with state dir %s, not %s", o.Account, rec.StateDir, o.StateDir)
	}
	if len(rec.Steps) > 0 && rec.UnitPrefix != o.UnitPrefix {
		return rep, fmt.Errorf("install: %s was installed with unit prefix %q, not %q", o.Account, prefixOr(rec.UnitPrefix), prefixOr(o.UnitPrefix))
	}
	rec.UnitPrefix = o.UnitPrefix
	in.rec = rec
	steps := []func() error{in.account, in.subids, in.kvm, in.delegation, in.apparmor, in.nodePassword, in.stateLayout,
		in.unitFiles, in.linger, in.enable, in.checkHost}
	for _, s := range steps {
		if err := s(); err != nil {
			return rep, err
		}
		if err := in.saveRecord(); err != nil {
			return rep, err
		}
	}
	if o.DryRun {
		fmt.Fprintf(in.out(), "dry run: %d change(s) computed, nothing written\n", len(rep.Changes))
	}
	return rep, nil
}

func (in *installer) account() error {
	name := in.o.Account
	a, ok, err := in.sys.LookupAccount(name)
	if err != nil {
		return err
	}
	if ok {
		switch {
		case in.rec.Steps[StepAccount] == OutcomeCreated || in.rec.Steps[StepAccount] == OutcomeAdopted:
			in.step(StepAccount, OutcomeFound, fmt.Sprintf("%s uid %d (recorded %s)", name, a.UID, in.rec.Steps[StepAccount]))
		case !in.o.AdoptAccount:
			return fmt.Errorf("%w: %s (uid %d) exists and %s does not record it; pass --adopt-account only if this account is meant for socialweb-computer",
				ErrForeignAccount, name, a.UID, RecordPath(name))
		default:
			in.step(StepAccount, OutcomeAdopted, fmt.Sprintf("%s uid %d adopted (--adopt-account)", name, a.UID))
		}
		in.acct, in.known = a, true
		in.rec.UID = a.UID
		return nil
	}
	err = in.change(fmt.Sprintf("create system account %s (home %s, no password, no login shell)", name, in.o.StateDir), func() error {
		return in.sys.CreateAccount(name, in.o.StateDir)
	})
	if err != nil {
		return err
	}
	in.step(StepAccount, OutcomeCreated, name)
	if in.o.DryRun {
		in.acct = Account{Name: name, UID: -1, GID: -1, Home: in.o.StateDir}
		return nil
	}
	if a, ok, err = in.sys.LookupAccount(name); err != nil || !ok {
		return fmt.Errorf("install: %s was created but does not resolve: %v", name, err)
	}
	in.acct, in.known = a, true
	in.rec.UID = a.UID
	return nil
}

func (in *installer) uidLabel() string {
	if !in.known {
		return "<uid>"
	}
	return strconv.Itoa(in.acct.UID)
}

func maxEnd(contents ...string) uint64 {
	var end uint64
	for _, c := range contents {
		for _, e := range hostcontract.ParseSubids(c) {
			if e.End() > end {
				end = e.End()
			}
		}
	}
	return end
}

func NextRangeStart(subuid, subgid string) uint64 {
	start := maxEnd(subuid, subgid)
	if start < SubidFloor {
		start = SubidFloor
	}
	return (start + hostcontract.VMSlotIDs - 1) / hostcontract.VMSlotIDs * hostcontract.VMSlotIDs
}

func (in *installer) subids() error {
	if in.o.Role != hostcontract.RoleBidder {
		return nil
	}
	files := []struct{ step, host string }{{StepSubuid, "/etc/subuid"}, {StepSubgid, "/etc/subgid"}}
	sub, sg := in.read("/etc/subuid"), in.read("/etc/subgid")
	start := NextRangeStart(sub, sg)
	count := uint64(in.o.VMSlots) * hostcontract.VMSlotIDs
	for _, f := range files {
		content := in.read(f.host)
		if r, ok := hostcontract.AccountRange(content, in.o.Account, in.acct.UID); ok {
			in.step(f.step, OutcomeFound, fmt.Sprintf("%d-%d", r.Start, r.End()-1))
			continue
		}
		line := fmt.Sprintf("%s:%d:%d\n", in.o.Account, start, count)
		next := content
		if next != "" && !strings.HasSuffix(next, "\n") {
			next += "\n"
		}
		next += line
		full := in.path(f.host)
		mode := os.FileMode(0o644)
		if st, err := os.Stat(full); err == nil {
			mode = st.Mode().Perm()
		}
		err := in.change(fmt.Sprintf("reserve %s in %s (%d VM slots of %d ids)", strings.TrimSpace(line), f.host, in.o.VMSlots, hostcontract.VMSlotIDs), func() error {
			return writeAtomic(full, []byte(next), mode)
		})
		if err != nil {
			return err
		}
		if in.rec.SubidLines == nil {
			in.rec.SubidLines = map[string]string{}
		}
		in.rec.SubidLines[f.host] = line
		in.step(f.step, OutcomeCreated, strings.TrimSpace(line))
	}
	return nil
}

func (in *installer) kvm() error {
	if in.o.Role != hostcontract.RoleBidder {
		return nil
	}
	gid, member := hostcontract.GroupMembership(in.read("/etc/group"), KVMGroup, in.o.Account)
	if gid < 0 {
		return fmt.Errorf("install: no %s group in /etc/group: load kvm_intel or kvm_amd and install the distribution's kvm/udev rules (never chmod /dev/kvm)", KVMGroup)
	}
	if member {
		in.step(StepKVM, OutcomeFound, fmt.Sprintf("%s in %s (gid %d)", in.o.Account, KVMGroup, gid))
	} else {
		if err := in.change("add "+in.o.Account+" to group "+KVMGroup+" (before the user manager first starts)", func() error {
			return in.sys.AddToGroup(in.o.Account, KVMGroup)
		}); err != nil {
			return err
		}
		in.step(StepKVM, OutcomeCreated, fmt.Sprintf("gid %d", gid))
	}
	if !in.known {
		return nil
	}
	if in.o.Staged {
		return nil
	}
	pid, groups, notRunning := hostcontract.ManagerGroups(in.o.Root, in.acct.UID)
	if notRunning != "" {
		return nil
	}
	for _, g := range groups {
		if g == gid {
			return nil
		}
	}
	if err := in.saveRecord(); err != nil {
		return err
	}
	return fmt.Errorf("%w: user@%d (pid %d) started before %s joined %s; restart user@%d - this stops every VM - or reboot, then re-run install (install never restarts it)",
		ErrManagerWithoutKVM, in.acct.UID, pid, in.o.Account, KVMGroup, in.acct.UID)
}

const statePlaceholder = "{{.State}}"

func render(name string, data unitData) ([]byte, error) {
	raw, err := templates.ReadFile("templates/" + name)
	if err != nil {
		return nil, err
	}
	out := strings.ReplaceAll(string(raw), statePlaceholder, data.State)
	if strings.Contains(out, "{{") {
		return nil, fmt.Errorf("install: template %s has a placeholder other than %s", name, statePlaceholder)
	}
	return []byte(out), nil
}

type unitData struct {
	State string
}

func RenderUnits(role hostcontract.Role, state, prefix string) (map[string][]byte, error) {
	out := map[string][]byte{}
	core, tmpl := table.CoreTemplate(prefix, table.RoleGateway), GatewayUnit
	if role == hostcontract.RoleBidder {
		core, tmpl = table.CoreTemplate(prefix, table.RoleBidder), BidderTemplate
	}
	for name, src := range map[string]string{SliceUnit: SliceUnit, core: tmpl} {
		b, err := render(src, unitData{State: state})
		if err != nil {
			return nil, err
		}
		out[name] = b
	}
	return out, nil
}

func DropIn() ([]byte, error) { return render(DropInName, unitData{}) }

func AppArmorProfile(state string) ([]byte, error) {
	return render(hostcontract.AppArmorProfileName, unitData{State: state})
}

func (in *installer) delegation() error {
	dir := "/etc/systemd/system/user@" + in.uidLabel() + ".service.d"
	content, err := DropIn()
	if err != nil {
		return err
	}
	if err := in.ensureDir(dir, 0o755, 0, 0, true); err != nil {
		return err
	}
	changed, err := in.ensureFile(filepath.Join(dir, DropInName), content, 0o644, 0, 0)
	if err != nil {
		return err
	}
	if !changed {
		in.step(StepDelegation, OutcomeFound, dir)
		return nil
	}
	in.step(StepDelegation, OutcomeCreated, dir+"/"+DropInName+": Delegate="+hostcontract.DelegatedControllers)
	return in.change("systemctl daemon-reload (system manager, for the delegation drop-in)", func() error {
		return in.sys.ReloadSystemManager(in.ctx)
	})
}

func (in *installer) apparmor() error {
	if in.o.Role != hostcontract.RoleBidder {
		return nil
	}
	if strings.TrimSpace(in.read(RestrictUserns)) != "1" {
		in.step(StepAppArmor, OutcomeNotNeeded, "AppArmor does not restrict unprivileged user namespaces")
		return nil
	}
	content, err := AppArmorProfile(in.o.StateDir)
	if err != nil {
		return err
	}
	changed, err := in.ensureFile(AppArmorPath, content, 0o644, 0, 0)
	if err != nil {
		return err
	}
	if changed {
		in.step(StepAppArmor, OutcomeCreated, AppArmorPath)
	} else {
		in.step(StepAppArmor, OutcomeFound, AppArmorPath)
	}
	if !changed && hostcontract.AppArmorProfileLoaded(in.read(AppArmorLoaded), hostcontract.AppArmorProfileName) {
		return nil
	}
	return in.change("apparmor_parser -r "+AppArmorPath, func() error { return in.sys.LoadAppArmor(in.path(AppArmorPath)) })
}

func (in *installer) nodePassword() error {
	if in.o.Role != hostcontract.RoleGateway {
		return nil
	}
	full := in.path(NodePassword)
	if _, err := os.Stat(full); err == nil {
		in.step(StepPassword, OutcomeFound, NodePassword)
		if in.o.DryRun || !in.known {
			return nil
		}
		ou, og, err := in.sys.Owner(full)
		if err != nil {
			return err
		}
		if ou != in.acct.UID {
			if in.rec.PrevOwner == nil {
				in.rec.PrevOwner = map[string][2]int{}
			}
			if _, kept := in.rec.PrevOwner[NodePassword]; !kept {
				in.rec.PrevOwner[NodePassword] = [2]int{ou, og}
			}
			in.rec.set(StepPassword, OutcomeUpdated)
		}
		return in.ensureOwner(full, 0o600, in.acct.UID, in.acct.GID)
	}
	if err := in.ensureDir(filepath.Dir(NodePassword), 0o755, 0, 0, true); err != nil {
		return err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	if _, err := in.ensureFile(NodePassword, []byte(hex.EncodeToString(secret)), 0o600, in.acct.UID, in.acct.GID); err != nil {
		return err
	}
	in.step(StepPassword, OutcomeCreated, NodePassword+" (0600, the k3s node password)")
	return nil
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (in *installer) stateLayout() error {
	uid, gid := in.acct.UID, in.acct.GID
	state := in.o.StateDir
	if err := in.ensureDir(filepath.Dir(state), 0o755, 0, 0, true); err != nil {
		return err
	}
	_, statErr := os.Stat(in.path(state))
	if err := in.ensureDir(state, 0o700, uid, gid, true); err != nil {
		return err
	}
	if errors.Is(statErr, fs.ErrNotExist) {
		in.step(StepStateDir, OutcomeCreated, state)
	} else {
		in.step(StepStateDir, OutcomeFound, state)
	}
	for _, d := range []string{"bin", "runtime"} {
		if err := in.ensureDir(filepath.Join(state, d), 0o755, uid, gid, false); err != nil {
			return err
		}
	}
	if err := in.binary(); err != nil {
		return err
	}
	return in.config()
}

func (in *installer) binary() error {
	sum, err := fileSHA256(in.o.Binary)
	if err != nil {
		return fmt.Errorf("install: hashing %s: %w", in.o.Binary, err)
	}
	bin := filepath.Join(in.o.StateDir, "bin")
	current := filepath.Join(bin, "current")
	if target, err := os.Readlink(in.path(current)); err == nil {
		if _, err := os.Stat(in.path(filepath.Join(bin, target, CoreName))); err == nil {
			detail := "bin/current -> " + target
			if target != sum {
				detail += "; this binary is " + sum + ": use upgrade to switch"
			}
			in.step(StepBinary, OutcomeFound, detail)
			return nil
		}
	}
	dir := filepath.Join(bin, sum)
	if err := in.ensureDir(dir, 0o755, in.acct.UID, in.acct.GID, false); err != nil {
		return err
	}
	dst := filepath.Join(dir, CoreName)
	if have, _ := fileSHA256(in.path(dst)); have != sum {
		if err := in.change("copy "+in.o.Binary+" to "+dst, func() error { return copyFile(in.o.Binary, in.path(dst), 0o755) }); err != nil {
			return err
		}
	}
	if !in.o.DryRun {
		if err := in.ensureOwner(in.path(dst), 0o755, in.acct.UID, in.acct.GID); err != nil {
			return err
		}
	}
	if err := in.change("point "+current+" at "+sum, func() error { return retarget(in.path(current), sum) }); err != nil {
		return err
	}
	in.step(StepBinary, OutcomeCreated, "bin/"+sum+"/"+CoreName)
	return nil
}

func retarget(link, target string) error {
	tmp := link + ".next"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, link)
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".partial"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func (in *installer) config() error {
	p := filepath.Join(in.o.StateDir, "config.json")
	cfg := map[string]any{}
	if b, err := os.ReadFile(in.path(p)); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return fmt.Errorf("install: %s is not valid JSON: %w", p, err)
		}
	}
	cfg["host-backend"] = string(hostsvc.BackendSystemdUser)
	cfg["state-dir"] = in.o.StateDir
	cfg["asset-cache"] = filepath.Join(in.o.StateDir, "runtime")
	if in.o.UnitPrefix != "" {
		cfg["unit-prefix"] = in.o.UnitPrefix
	} else {
		delete(cfg, "unit-prefix")
	}
	if in.o.Role == hostcontract.RoleGateway {
		for k, v := range GatewayListen {
			if s, _ := cfg[k].(string); s == "" {
				cfg[k] = v
			}
		}
	}
	for k, v := range in.o.Settings {
		if v != "" {
			cfg[k] = v
		}
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	changed, err := in.ensureFile(p, append(b, '\n'), 0o644, in.acct.UID, in.acct.GID)
	if err != nil {
		return err
	}
	if changed {
		in.step(StepConfig, OutcomeCreated, p+": host-backend "+string(hostsvc.BackendSystemdUser))
	} else {
		in.step(StepConfig, OutcomeFound, p)
	}
	return nil
}

func (in *installer) unitDir() string { return filepath.Join(in.acct.Home, UserUnitDir) }

func (in *installer) unitFiles() error {
	units, err := RenderUnits(in.o.Role, in.o.StateDir, in.o.UnitPrefix)
	if err != nil {
		return err
	}
	dir := in.unitDir()
	for _, d := range []string{filepath.Join(in.acct.Home, ".config"), filepath.Join(in.acct.Home, ".config/systemd"), dir} {
		if err := in.ensureDir(d, 0o755, in.acct.UID, in.acct.GID, false); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(units))
	for n := range units {
		names = append(names, n)
	}
	sort.Strings(names)
	any := false
	for _, n := range names {
		changed, err := in.ensureFile(filepath.Join(dir, n), units[n], 0o644, in.acct.UID, in.acct.GID)
		if err != nil {
			return err
		}
		in.rec.addUnitFile(filepath.Join(dir, n))
		any = any || changed
	}
	boot, bootChanged, err := in.bootstrapFiles(dir)
	if err != nil {
		return err
	}
	names = append(names, boot...)
	any = any || bootChanged
	in.unitsUp = !any
	if any {
		in.step(StepUnitFiles, OutcomeCreated, strings.Join(names, " "))
	} else {
		in.step(StepUnitFiles, OutcomeFound, strings.Join(names, " "))
	}
	return nil
}

func (in *installer) linger() error {
	p := filepath.Join(LingerDir, in.o.Account)
	if _, err := os.Stat(in.path(p)); err == nil {
		in.step(StepLinger, OutcomeFound, p)
		return nil
	}
	if err := in.change("loginctl enable-linger "+in.o.Account+" (user@"+in.uidLabel()+" first starts here)", func() error {
		return in.sys.SetLinger(in.ctx, in.acct, true)
	}); err != nil {
		return err
	}
	in.step(StepLinger, OutcomeCreated, p)
	return nil
}

func (in *installer) enable() error {
	units := CoreUnits(in.o.Role, in.o.UnitPrefix, in.o.Handles)
	if in.o.DryRun && !in.known {
		for _, u := range units {
			in.rep.Changes = append(in.rep.Changes, "enable --now "+u)
			fmt.Fprintf(in.out(), "would enable --now %s as %s\n", u, in.o.Account)
		}
		return nil
	}
	if in.o.DryRun {
		if _, err := os.Stat(in.path(filepath.Join(LingerDir, in.o.Account))); err != nil {
			for _, u := range units {
				in.rep.Changes = append(in.rep.Changes, "enable --now "+u)
				fmt.Fprintf(in.out(), "would enable --now %s as %s\n", u, in.o.Account)
			}
			return nil
		}
	}
	um, err := in.sys.User(in.ctx, in.acct)
	if err != nil {
		return fmt.Errorf("install: reaching user@%d: %w", in.acct.UID, err)
	}
	defer um.Close()
	var todo []string
	for _, u := range units {
		_, linkErr := os.Lstat(in.path(filepath.Join(in.unitDir(), WantsDir, u)))
		active, err := um.Active(in.ctx, u)
		if err != nil {
			return err
		}
		if linkErr != nil || !active {
			todo = append(todo, u)
		}
	}
	in.rec.addHandles(in.o.Handles)
	if len(todo) == 0 && in.unitsUp {
		in.step(StepUnits, OutcomeFound, strings.Join(units, " "))
		return nil
	}
	if err := in.change("systemctl --user daemon-reload as "+in.o.Account, func() error { return um.Reload(in.ctx) }); err != nil {
		return err
	}
	if err := in.enableBootstrap(um); err != nil {
		return err
	}
	if len(todo) > 0 {
		if err := in.change("enable --now "+strings.Join(todo, " ")+" as "+in.o.Account, func() error { return um.EnableNow(in.ctx, todo) }); err != nil {
			return err
		}
	}
	in.step(StepUnits, OutcomeCreated, strings.Join(units, " "))
	return nil
}

func (in *installer) checkHost() error {
	if in.o.DryRun {
		fmt.Fprintf(in.out(), "would run check-host -role %s as %s\n", in.o.Role, in.o.Account)
		return nil
	}
	exe := filepath.Join(in.o.StateDir, "bin", "current", CoreName)
	if err := in.sys.CheckHost(in.ctx, in.acct, exe, in.o.Role, in.o.StateDir); err != nil {
		return fmt.Errorf("install: check-host as %s: %w; fix what it names and re-run install, which completes from here", in.o.Account, err)
	}
	in.rep.Steps = append(in.rep.Steps, StepResult{Name: StepCheckHost, Outcome: OutcomeFound, Detail: "the host contract holds"})
	return nil
}
