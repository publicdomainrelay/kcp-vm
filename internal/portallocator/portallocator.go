package portallocator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostos"
)

const (
	DefaultFirst = 20000
	DefaultLast  = 29999
)

type Range struct {
	First int
	Last  int
}

func (r Range) Contains(p int) bool { return p >= r.First && p <= r.Last }

func (r Range) Overlaps(o Range) bool { return r.First <= o.Last && o.First <= r.Last }

func (r Range) Empty() bool { return r == Range{} || r.Last < r.First }

func (r Range) String() string { return fmt.Sprintf("%d-%d", r.First, r.Last) }

type Purpose string

const (
	PurposePostgres Purpose = "postgres"

	PurposeOpenBao Purpose = "openbao"

	PurposeKine Purpose = "kine"

	PurposeK3sSecure Purpose = "k3s-secure"

	PurposeK3sControllerManager      Purpose = "k3s-controller-manager"
	PurposeK3sScheduler              Purpose = "k3s-scheduler"
	PurposeK3sCloudControllerManager Purpose = "k3s-cloud-controller-manager"
)

var retiredPurposes = map[Purpose]bool{
	"ssh-server": true,
	"ssh-node":   true,
	"advertise":  true,
	"kubelet":    true,
}

var purposeWidth = map[Purpose]int{
	PurposePostgres:                  1,
	PurposeOpenBao:                   1,
	PurposeKine:                      1,
	PurposeK3sSecure:                 2,
	PurposeK3sControllerManager:      1,
	PurposeK3sScheduler:              1,
	PurposeK3sCloudControllerManager: 1,
}

func (p Purpose) Width() int { return purposeWidth[p] }

var gatewayShared = []Range{
	{6443, 6443},
	{2379, 2380},
	{9100, 9103},
}

type Lease struct {
	Port    int     `json:"port"`
	Width   int     `json:"width"`
	Purpose Purpose `json:"purpose"`
	Cluster string  `json:"cluster"`
	Account string  `json:"account,omitempty"`

	Instance string `json:"instance,omitempty"`

	AllocatedAt time.Time `json:"allocatedAt"`
}

func (l Lease) Covers(p int) bool { return p >= l.Port && p < l.Port+l.Width }

const leaseFileVersion = 1

type leaseFile struct {
	Version int     `json:"version"`
	Leases  []Lease `json:"leases"`
}

type Config struct {
	LeasePath string

	Range Range

	Ephemeral *Range

	Probe func(port int) bool

	ReclaimGrace time.Duration

	Now func() time.Time
}

type Allocator struct {
	path  string
	rng   Range
	probe func(int) bool
	grace time.Duration
	now   func() time.Time

	mu     sync.Mutex
	leases map[leaseKey]Lease
}

type leaseKey struct {
	cluster string
	purpose Purpose
}

type RangeConflictError struct {
	Range  Range
	Reason string
	Detail string
}

func (e *RangeConflictError) Error() string {
	return fmt.Sprintf("portallocator: range %s is unusable: %s (%s)", e.Range, e.Reason, e.Detail)
}

type ExhaustedError struct {
	Range Range
	Width int
}

func (e *ExhaustedError) Error() string {
	return fmt.Sprintf("portallocator: no free block of %d consecutive ports in %s", e.Width, e.Range)
}

func Open(cfg Config) (*Allocator, error) {
	if cfg.LeasePath == "" {
		return nil, errors.New("portallocator: LeasePath is required; the host-side lease file is the authority")
	}
	rng := cfg.Range
	if rng.Empty() {
		rng = Range{DefaultFirst, DefaultLast}
	}
	if rng.First < 1024 {
		return nil, &RangeConflictError{Range: rng,
			Reason: "starts below 1024, and the carve must sit above the privileged range",
			Detail: "every port here is bound by a non-root gateway process"}
	}

	eph := Range{}
	if cfg.Ephemeral != nil {
		eph = *cfg.Ephemeral
	} else {
		live, err := ReadEphemeralRange()
		if err != nil {
			return nil, fmt.Errorf("portallocator: refusing to start: %w", err)
		}
		eph = live
	}
	if err := AssertRangeDisjoint(rng, eph); err != nil {
		return nil, err
	}
	for _, shared := range gatewayShared {
		if rng.Overlaps(shared) {
			return nil, &RangeConflictError{Range: rng,
				Reason: "swallows a port the GATEWAY owns once for the whole host, outside the allocator",
				Detail: fmt.Sprintf("%s is outside the per-cluster space; the kcp secure port, kcp's etcd and rustfs must never be handed to a cluster", shared),
			}
		}
	}

	a := &Allocator{
		path:   cfg.LeasePath,
		rng:    rng,
		probe:  cfg.Probe,
		grace:  cfg.ReclaimGrace,
		now:    cfg.Now,
		leases: map[leaseKey]Lease{},
	}
	if a.probe == nil {
		a.probe = probeBound
	}
	if a.now == nil {
		a.now = time.Now
	}
	if err := a.load(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Allocator) Range() Range { return a.rng }

func (a *Allocator) lockPath() string { return a.path + ".lock" }

func (a *Allocator) withLock(how int, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return fmt.Errorf("portallocator: lease dir: %w", err)
	}
	f, err := os.OpenFile(a.lockPath(), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("portallocator: lease lock %s: %w", a.lockPath(), err)
	}
	defer f.Close()
	if err := hostos.Lock(f, how); err != nil {
		return fmt.Errorf("portallocator: locking %s: %w", a.lockPath(), err)
	}
	defer func() { _ = hostos.Unlock(f) }()
	return fn()
}

func (a *Allocator) load() error {
	leases, err := a.readFile()
	if err != nil {
		return err
	}
	a.leases = leases
	return nil
}

func (a *Allocator) readFile() (map[leaseKey]Lease, error) {
	data, err := os.ReadFile(a.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[leaseKey]Lease{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("portallocator: reading lease file %s: %w", a.path, err)
	}
	var f leaseFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("portallocator: lease file %s is unreadable, refusing to start with an empty store over it: %w", a.path, err)
	}
	if f.Version != leaseFileVersion {
		return nil, fmt.Errorf("portallocator: lease file %s is version %d, this build writes %d", a.path, f.Version, leaseFileVersion)
	}
	leases := make(map[leaseKey]Lease, len(f.Leases))
	for _, l := range f.Leases {
		if retiredPurposes[l.Purpose] {
			continue
		}
		leases[leaseKey{l.Cluster, l.Purpose}] = l
	}
	return leases, nil
}

type Request struct {
	Cluster  string
	Account  string
	Instance string
	Purpose  Purpose
}

func (a *Allocator) Allocate(req Request) (Lease, error) {
	if req.Cluster == "" {
		return Lease{}, errors.New("portallocator: Cluster is required; the lease is keyed on CR presence")
	}
	width := req.Purpose.Width()
	if width == 0 {
		return Lease{}, fmt.Errorf("portallocator: unknown Purpose %q", req.Purpose)
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	var granted Lease
	err := a.withLock(hostos.LockExclusive, func() error {
		if err := a.load(); err != nil {
			return err
		}

		if found := FindDuplicatePorts(a.snapshot()); len(found) > 0 {
			return errors.New(strings.Join(found, "; "))
		}

		key := leaseKey{req.Cluster, req.Purpose}
		if held, ok := a.leases[key]; ok {
			held.Instance = req.Instance
			a.leases[key] = held
			if err := a.save(); err != nil {
				return err
			}
			granted = held
			return nil
		}

		port, err := a.findFree(width)
		if err != nil {
			return err
		}
		lease := Lease{
			Port:        port,
			Width:       width,
			Purpose:     req.Purpose,
			Cluster:     req.Cluster,
			Account:     req.Account,
			Instance:    req.Instance,
			AllocatedAt: a.now(),
		}
		a.leases[key] = lease
		if err := a.save(); err != nil {
			delete(a.leases, key)
			return err
		}
		granted = lease
		return nil
	})
	if err != nil {
		return Lease{}, err
	}
	return granted, nil
}

func (a *Allocator) findFree(width int) (int, error) {
	for port := a.rng.First; port+width-1 <= a.rng.Last; port++ {
		if a.taken(port, width) {
			continue
		}
		if a.probe != nil && a.probe(port) {
			continue
		}
		return port, nil
	}
	return 0, &ExhaustedError{Range: a.rng, Width: width}
}

func (a *Allocator) taken(port, width int) bool {
	for _, l := range a.leases {
		for p := port; p < port+width; p++ {
			if l.Covers(p) {
				return true
			}
		}
	}
	return false
}

func (a *Allocator) Release(cluster string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.withLock(hostos.LockExclusive, func() error {
		if err := a.load(); err != nil {
			return err
		}
		for key := range a.leases {
			if key.cluster == cluster {
				delete(a.leases, key)
			}
		}
		return a.save()
	})
}

func (a *Allocator) ReleasePurpose(cluster string, purpose Purpose) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.withLock(hostos.LockExclusive, func() error {
		if err := a.load(); err != nil {
			return err
		}
		key := leaseKey{cluster, purpose}
		if _, ok := a.leases[key]; !ok {
			return nil
		}
		delete(a.leases, key)
		return a.save()
	})
}

func (a *Allocator) Leases() []Lease {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refresh()
	return a.snapshot()
}

func (a *Allocator) PortFor(cluster string, purpose Purpose) (Lease, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.refresh()
	l, ok := a.leases[leaseKey{cluster, purpose}]
	return l, ok
}

func (a *Allocator) refresh() {
	_ = a.withLock(hostos.LockShared, a.load)
}

func (a *Allocator) Reclaim(known map[string]bool) ([]Lease, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var freed []Lease
	err := a.withLock(hostos.LockExclusive, func() error {
		if err := a.load(); err != nil {
			return err
		}
		freed = LeasesToReclaim(a.snapshot(), known, a.now(), a.grace)
		for _, l := range freed {
			delete(a.leases, leaseKey{l.Cluster, l.Purpose})
		}
		if len(freed) == 0 {
			return nil
		}
		return a.save()
	})
	if err != nil {
		return nil, err
	}
	return freed, nil
}

func LeasesToReclaim(leases []Lease, known map[string]bool, now time.Time, grace time.Duration) []Lease {
	var out []Lease
	for _, l := range leases {
		if known[l.Cluster] {
			continue
		}
		if grace > 0 && now.Sub(l.AllocatedAt) < grace {
			continue
		}
		out = append(out, l)
	}
	return out
}

func (a *Allocator) snapshot() []Lease {
	out := make([]Lease, 0, len(a.leases))
	for _, l := range a.leases {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Cluster < out[j].Cluster
	})
	return out
}

func (a *Allocator) save() error {
	if err := os.MkdirAll(filepath.Dir(a.path), 0o700); err != nil {
		return fmt.Errorf("portallocator: lease dir: %w", err)
	}
	data, err := json.MarshalIndent(leaseFile{Version: leaseFileVersion, Leases: a.snapshot()}, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("portallocator: lease file: %w", err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("portallocator: lease file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("portallocator: lease file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("portallocator: lease file: %w", err)
	}
	return os.Rename(tmp, a.path)
}
