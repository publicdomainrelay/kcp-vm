package noderecord

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
)

type Liveness int

const (
	LivenessGone Liveness = iota

	LivenessOurs

	LivenessNotOurs
)

func (l Liveness) String() string {
	switch l {
	case LivenessOurs:
		return "ours"
	case LivenessNotOurs:
		return "not ours"
	case LivenessGone:
		return "gone"
	}
	return fmt.Sprintf("Liveness(%d)", int(l))
}

func ProcStart(pid int) (uint64, error) {
	start, err := hostos.ProcStart(pid)
	if err != nil {
		return 0, fmt.Errorf("noderecord: reading the start time of pid %d, which is what "+
			"separates the process a record names from whatever later takes over its pid number: %w",
			pid, err)
	}
	return start, nil
}

func LivenessOf(dataDir string, identities ...[]string) (Liveness, error) {
	rec, err := Read(dataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return LivenessGone, nil
	}
	if err != nil {
		return LivenessGone, err
	}
	live, _, err := check(rec, identities)
	return live, err
}

func check(rec Record, identities [][]string) (Liveness, bool, error) {
	start, err := hostos.ProcStart(rec.PID)
	if errors.Is(err, fs.ErrNotExist) {
		return LivenessGone, false, nil
	}
	if err != nil {
		return LivenessGone, false, err
	}
	if start != rec.StartTime {
		return LivenessNotOurs, false, nil
	}
	if len(rec.Identity) == 0 || len(identities) == 0 {
		return LivenessNotOurs, true, nil
	}
	argv, err := hostos.Cmdline(rec.PID)
	if errors.Is(err, fs.ErrNotExist) {
		return LivenessGone, false, nil
	}
	if err != nil {
		return LivenessGone, false, err
	}
	for _, identity := range identities {
		if Matches(argv, identity) {
			return LivenessOurs, true, nil
		}
	}
	return LivenessNotOurs, true, nil
}

type ReapOptions struct {
	Cluster string

	NodeName string

	DataDir string

	Identity []string

	HolderIdentity []string

	Grace time.Duration

	Log *slog.Logger
}

func Reap(ctx context.Context, opts ReapOptions) error {
	identities := make([][]string, 0, 2)
	if len(opts.Identity) > 0 {
		identities = append(identities, opts.Identity)
	}
	if len(opts.HolderIdentity) > 0 {
		identities = append(identities, opts.HolderIdentity)
	}
	if opts.NodeName == "" || opts.DataDir == "" || len(identities) == 0 {
		return fmt.Errorf("%w: reaping needs the node's name, its data dir and the identity its "+
			"substrate gives its process, because the record it reads is named by the data dir and "+
			"is only ever signalled when the process behind it matches that identity", ErrIncomplete)
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	rec, err := Read(opts.DataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	live, alive, err := check(rec, identities)
	if err != nil {
		return fmt.Errorf("noderecord: deciding whether the process recorded for node %s at pid %d is this node's own: %w -- nothing was signalled, because a signal aimed at a pid we could not verify is a signal aimed at whatever now holds that number", opts.NodeName, rec.PID, err)
	}
	switch {
	case live == LivenessOurs:
		if err := killPID(ctx, opts.NodeName, rec.PID, opts.Grace); err != nil {
			return fmt.Errorf("noderecord: stopping the process recorded for node %s at pid %d: %w -- the record at %s is left where it is, because a process we could not stop is still holding this node's resources and still naming this node at the cluster's apiserver, and a cleared record would leave both of those without an owner", opts.NodeName, rec.PID, err, RecordPath(opts.DataDir))
		}
		log.Info("noderecord: reaped the node's process", "cluster", opts.Cluster, "node", opts.NodeName, "pid", rec.PID)
	case alive:
		log.Warn("noderecord: the pid in this node's record does not match this node's identity, so it is left alone",
			"cluster", opts.Cluster, "node", opts.NodeName, "pid", rec.PID,
			"recordedStart", rec.StartTime, "recordedIdentity", rec.Identity,
			"record", RecordPath(opts.DataDir))
		return fmt.Errorf("noderecord: node %s: %w", opts.NodeName, ErrNotOurs)
	default:
		log.Info("noderecord: the process recorded for this node had already exited",
			"cluster", opts.Cluster, "node", opts.NodeName, "pid", rec.PID)
	}
	return Remove(opts.DataDir)
}

const DefaultKillGrace = 10 * time.Second

func killPID(ctx context.Context, nodeName string, pid int, grace time.Duration) error {
	if grace <= 0 {
		grace = DefaultKillGrace
	}
	if err := hostos.Signal(pid, false); err != nil {
		return fmt.Errorf("SIGTERM to pid %d: %w", pid, err)
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		alive, err := hostos.Alive(pid)
		if err != nil {
			return err
		}
		if !alive {
			return nil
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := hostos.Signal(pid, true); err != nil {
		return fmt.Errorf("node %s: pid %d survived %s of SIGTERM and the forced stop that follows it did "+
			"not land either: %w", nodeName, pid, grace, err)
	}
	return nil
}
