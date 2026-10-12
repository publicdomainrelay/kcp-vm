package hostsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"syscall"
	"time"
)

const NamePrefix = "socialweb-"

const ServiceSuffix = ".service"

const DefaultSlice = "socialweb.slice"

const MinSystemdVersion = 253

const DefaultCallTimeout = 10 * time.Second

var ErrUnsupported = errors.New("hostsvc: no service manager backend on this OS")

var ErrUnreachable = errors.New("hostsvc: service manager unreachable")

var ErrSystemdTooOld = fmt.Errorf("hostsvc: systemd older than %d (GetUnitByPIDFD is required)", MinSystemdVersion)

type ActiveState string

const (
	Active       ActiveState = "active"
	Activating   ActiveState = "activating"
	Deactivating ActiveState = "deactivating"
	Reloading    ActiveState = "reloading"
	Inactive     ActiveState = "inactive"
	Failed       ActiveState = "failed"
	NotFound     ActiveState = "not-found"
)

type Output struct {
	StdoutFile string
	StderrFile string
}

type Limits struct {
	MemoryMax       uint64
	CPUWeight       uint64
	CPUQuotaPercent int
	TasksMax        uint64
}

type UnitSpec struct {
	Name        string
	Description string
	Exec        []string
	Env         []string
	WorkDir     string
	Output      Output
	Limits      Limits
	OOMPolicy   string
	KillSignal  syscall.Signal
	StopTimeout time.Duration
	After       []string
	Wants       []string
	Slice       string
}

type UnitStatus struct {
	Name           string
	State          ActiveState
	SubState       string
	Result         string
	MainPID        int
	InvocationID   string
	ExecMainCode   int32
	ExecMainStatus int32
	ActiveSince    time.Time
	Adopted        bool
}

type Verdict int

const (
	VerdictUnknown Verdict = iota
	VerdictRunning
	VerdictLost
)

func (v Verdict) String() string {
	switch v {
	case VerdictRunning:
		return "Running"
	case VerdictLost:
		return "Lost"
	}
	return "Unknown"
}

type LostReason string

const (
	LostHostRebooted     LostReason = "HostRebooted"
	LostUnitGone         LostReason = "UnitGone"
	LostUnitExited       LostReason = "UnitExited"
	LostUnitFailed       LostReason = "UnitFailed"
	LostOOMKilled        LostReason = "OOMKilled"
	LostIdentityMismatch LostReason = "IdentityMismatch"
)

type Identity struct {
	Unit         string
	InvocationID string
	PID          int
	StartTicks   uint64
	BootID       string
}

type Observation struct {
	Verdict Verdict
	Reason  LostReason
	Detail  string
	Status  UnitStatus
	Stopped bool
}

type Manager interface {
	Start(ctx context.Context, spec UnitSpec) (UnitStatus, error)
	Inspect(ctx context.Context, name string) (UnitStatus, error)
	Stop(ctx context.Context, name string) error
	ResetFailed(ctx context.Context, name string) error
	List(ctx context.Context, prefix string) ([]UnitStatus, error)
	Identify(ctx context.Context, name string, pid int) (Identity, error)
	Check(ctx context.Context, want Identity) (Observation, error)
	Close() error
}

func ValidateName(name string) error {
	if !strings.HasPrefix(name, NamePrefix) || !strings.HasSuffix(name, ServiceSuffix) {
		return fmt.Errorf("hostsvc: unit %q is not ours: every unit this package starts is named %s<kind>-<owner>-<name>%s",
			name, NamePrefix, ServiceSuffix)
	}
	if strings.ContainsAny(name, "*?[]/\\ ") {
		return fmt.Errorf("hostsvc: unit %q carries a glob or path character", name)
	}
	return nil
}

func ValidatePrefix(prefix string) error {
	if !strings.HasPrefix(prefix, NamePrefix) || len(prefix) <= len(NamePrefix) {
		return fmt.Errorf("hostsvc: list prefix %q must start with %s and name a kind", prefix, NamePrefix)
	}
	if strings.ContainsAny(prefix, "*?[]/\\ ") {
		return fmt.Errorf("hostsvc: list prefix %q carries a glob or path character", prefix)
	}
	return nil
}

func ValidateSpec(spec UnitSpec) error {
	if err := ValidateName(spec.Name); err != nil {
		return err
	}
	if len(spec.Exec) == 0 || !strings.HasPrefix(spec.Exec[0], "/") {
		return fmt.Errorf("hostsvc: unit %s needs an absolute executable path, got %q", spec.Name, spec.Exec)
	}
	if spec.Slice == "app.slice" || strings.HasPrefix(spec.Slice, "app-") {
		return fmt.Errorf("hostsvc: unit %s may not run in %s: systemd-oomd kills by memory pressure there",
			spec.Name, spec.Slice)
	}
	if spec.Slice != "" && !strings.HasSuffix(spec.Slice, ".slice") {
		return fmt.Errorf("hostsvc: unit %s names slice %q, which is not a .slice", spec.Name, spec.Slice)
	}
	if spec.Limits.CPUQuotaPercent < 0 {
		return fmt.Errorf("hostsvc: unit %s asks for a negative CPU quota", spec.Name)
	}
	return nil
}
