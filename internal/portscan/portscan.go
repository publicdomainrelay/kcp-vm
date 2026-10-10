package portscan

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

type Survivors map[string][]string

type Drop struct {
	Cluster string
	Port    int
	PID     int

	Argv []string

	Reason string
}

type Stack struct {
	Cluster string

	Spec clusterstack.Spec

	Prefix []string

	Argvs [][]string
}

var ErrNoAttribution = errors.New(
	"portscan: refusing to scan: no cluster stack was supplied to attribute a bound process " +
		"to, and a process that is not ours must never become a kill target")

func Scan(leases []portallocator.Lease, stacks []Stack) (Survivors, []Drop, error) {
	expected, err := expectations(stacks)
	if err != nil {
		return nil, nil, err
	}
	stream, err := listeners()
	if err != nil {
		return nil, nil, err
	}
	datagram, err := datagramListeners()
	if err != nil {
		return nil, nil, err
	}
	survivors, dropped := scan(leases, expected, byTransport(leases, stream, datagram), cmdline)
	return survivors, dropped, nil
}

func ListenerPIDs(port int) ([]int, error) {
	stream, err := listeners()
	if err != nil {
		return nil, err
	}
	return append([]int(nil), stream[port]...), nil
}

func byTransport(leases []portallocator.Lease, stream, datagram map[int][]int) map[int][]int {
	out := map[int][]int{}
	for _, l := range leases {
		owners := stream
		if l.Purpose.Datagram() {
			owners = datagram
		}
		for p := l.Port; p < l.Port+l.Width; p++ {
			if pids, ok := owners[p]; ok {
				out[p] = pids
			}
		}
	}
	return out
}

func scan(
	leases []portallocator.Lease,
	expected map[string][][]string,
	owners map[int][]int,
	argvOf func(pid int) ([]string, error),
) (Survivors, []Drop) {
	kept, dropped := attribute(leases, expected, owners, argvOf)
	return associate(leases, kept), dropped
}

type claim struct {
	Cluster string
	Port    int
}

type verdict struct {
	Cluster string
	PID     int
}

func expectations(stacks []Stack) (map[string][][]string, error) {
	if len(stacks) == 0 {
		return nil, ErrNoAttribution
	}
	out := make(map[string][][]string, len(stacks))
	for _, s := range stacks {
		if s.Cluster == "" {
			return nil, errors.New("portscan: a Stack with no Cluster names no lease to attribute to")
		}
		argvs, err := s.spawned()
		if err != nil {
			return nil, err
		}
		if len(argvs) == 0 {
			return nil, fmt.Errorf("portscan: %s: the stack has no component with an argv", s.Cluster)
		}
		out[s.Cluster] = argvs
	}
	return out, nil
}

func (s Stack) spawned() ([][]string, error) {
	if len(s.Argvs) > 0 {
		out := make([][]string, 0, len(s.Argvs))
		for _, argv := range s.Argvs {
			if len(argv) == 0 {
				return nil, fmt.Errorf(
					"portscan: %s: a component with an EMPTY argv identifies nothing, and an "+
						"empty vector matches nothing, so the stack this caller described is not "+
						"the one it spawns", s.Cluster)
			}
			out = append(out, append(append([]string{}, s.Prefix...), argv...))
		}
		return out, nil
	}
	var out [][]string
	for _, c := range procgroup.StackOrder {
		argv, err := s.Spec.Argv(c)
		if errors.Is(err, clusterstack.ErrNotAProcess) {
			continue
		}
		if errors.Is(err, clusterstack.ErrMissingExternalSecrets) && s.Spec.ExternalSecrets == (clusterstack.ExternalSecrets{}) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("portscan: %s: %w", s.Cluster, err)
		}
		out = append(out, append(append([]string{}, s.Prefix...), argv...))
	}
	return out, nil
}

func attribute(
	leases []portallocator.Lease,
	expected map[string][][]string,
	owners map[int][]int,
	argvOf func(pid int) ([]string, error),
) (map[claim][]int, []Drop) {
	kept := map[claim][]int{}
	var dropped []Drop
	verdicts := map[verdict]bool{}

	for _, l := range leases {
		want := expected[l.Cluster]
		for p := l.Port; p < l.Port+l.Width; p++ {
			held := claim{Cluster: l.Cluster, Port: p}
			for _, pid := range owners[p] {
				key := verdict{Cluster: l.Cluster, PID: pid}
				mine, decided := verdicts[key]
				if !decided {
					argv, err := argvOf(pid)
					mine = err == nil && attributed(want, argv)
					verdicts[key] = mine
					if !mine {
						dropped = append(dropped, Drop{
							Cluster: l.Cluster,
							Port:    p,
							PID:     pid,
							Argv:    argv,
							Reason:  whyNot(err, want),
						})
					}
				}
				if mine {
					kept[held] = append(kept[held], pid)
				}
			}
		}
	}
	sort.Slice(dropped, func(i, j int) bool {
		if dropped[i].Cluster != dropped[j].Cluster {
			return dropped[i].Cluster < dropped[j].Cluster
		}
		if dropped[i].Port != dropped[j].Port {
			return dropped[i].Port < dropped[j].Port
		}
		return dropped[i].PID < dropped[j].PID
	})
	return kept, dropped
}

func attributed(expected [][]string, argv []string) bool {
	return slices.ContainsFunc(expected, func(component []string) bool {
		return containsAll(argv, component)
	})
}

func containsAll(argv, tokens []string) bool {
	if len(tokens) == 0 {
		return false
	}
	for _, t := range tokens {
		if !slices.ContainsFunc(argv, func(a string) bool { return strings.Contains(a, t) }) {
			return false
		}
	}
	return true
}

func whyNot(err error, expected [][]string) string {
	if err != nil {
		return fmt.Sprintf("could not read the process's own command line: %v", err)
	}
	if len(expected) == 0 {
		return "no stack was supplied for this cluster, so its owner cannot be attributed at all"
	}
	return "the command line is not any of this cluster's components as the stack spawns them"
}

func associate(leases []portallocator.Lease, owners map[claim][]int) Survivors {
	byCluster := map[string]map[int]bool{}
	for _, l := range leases {
		for p := l.Port; p < l.Port+l.Width; p++ {
			for _, pid := range owners[claim{Cluster: l.Cluster, Port: p}] {
				if byCluster[l.Cluster] == nil {
					byCluster[l.Cluster] = map[int]bool{}
				}
				byCluster[l.Cluster][pid] = true
			}
		}
	}
	out := make(Survivors, len(byCluster))
	for cluster, pids := range byCluster {
		nums := make([]int, 0, len(pids))
		for pid := range pids {
			nums = append(nums, pid)
		}
		sort.Ints(nums)
		handles := make([]string, len(nums))
		for i, pid := range nums {
			handles[i] = strconv.Itoa(pid)
		}
		out[cluster] = handles
	}
	return out
}
