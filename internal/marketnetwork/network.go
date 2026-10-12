package marketnetwork

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
)

const FCWorkRootBase = "/var/tmp/swc-"

type Proc struct {
	PID     int    `json:"pid,omitempty"`
	Port    int    `json:"port,omitempty"`
	Address string `json:"address,omitempty"`
}

type Bidder struct {
	Name string `json:"name"`
	DID  string `json:"did,omitempty"`
	Port int    `json:"port,omitempty"`
}

type Network struct {
	ID         string            `json:"networkId"`
	Slot       int               `json:"slot"`
	PID        int               `json:"pid,omitempty"`
	StartedAt  string            `json:"startedAt,omitempty"`
	Dir        string            `json:"networkDir,omitempty"`
	SourceRoot string            `json:"sourceRoot,omitempty"`
	FCWorkRoot string            `json:"fcWorkRoot"`
	HostIP     string            `json:"hostIp,omitempty"`
	Kubeconfig string            `json:"kubeconfig,omitempty"`
	Processes  map[string]Proc   `json:"processes,omitempty"`
	Services   map[string]Proc   `json:"services,omitempty"`
	Bidders    []Bidder          `json:"bidders,omitempty"`
	Readings   map[string]any    `json:"readings,omitempty"`
	Checks     map[string]string `json:"checks,omitempty"`
}

var networkIDPattern = regexp.MustCompile(`^[a-z0-9]{1,12}$`)

func ValidNetworkID(id string) bool {
	return networkIDPattern.MatchString(id)
}

func FCWorkRootFor(id string) string {
	return FCWorkRootBase + id
}

func LoadNetwork(path string) (Network, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Network{}, err
	}
	var network Network
	if err := json.Unmarshal(body, &network); err != nil {
		return Network{}, fmt.Errorf("%s: %w", path, err)
	}
	return network, nil
}

func (b Network) Ports() []int {
	var ports []int
	for _, group := range []map[string]Proc{b.Processes, b.Services} {
		for _, p := range group {
			if p.Port > 0 {
				ports = append(ports, p.Port)
			}
		}
	}
	sort.Ints(ports)
	return ports
}

func (b Network) Validate() error {
	if !ValidNetworkID(b.ID) {
		return fmt.Errorf("market network id %q is not 1-12 characters of [a-z0-9]", b.ID)
	}
	if b.FCWorkRoot != FCWorkRootFor(b.ID) {
		return fmt.Errorf("market network %s keeps its guests under %s, not its own %s", b.ID, b.FCWorkRoot, FCWorkRootFor(b.ID))
	}
	owner := map[int]string{}
	for _, group := range []struct {
		kind  string
		procs map[string]Proc
	}{{"process", b.Processes}, {"service", b.Services}} {
		for name, p := range group.procs {
			if group.kind == "service" && p.Port <= 0 {
				return fmt.Errorf("market network %s: service %s never reported the port it bound", b.ID, name)
			}
			if p.Port <= 0 {
				continue
			}
			if other, taken := owner[p.Port]; taken {
				return fmt.Errorf("market network %s: %s and %s both claim port %d", b.ID, other, name, p.Port)
			}
			owner[p.Port] = name
		}
	}
	return nil
}

func OverlappingPorts(a, b Network) []int {
	seen := map[int]bool{}
	for _, p := range a.Ports() {
		seen[p] = true
	}
	var overlap []int
	for _, p := range b.Ports() {
		if seen[p] {
			overlap = append(overlap, p)
		}
	}
	return overlap
}
