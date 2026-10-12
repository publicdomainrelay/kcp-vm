package marketbed

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
)

const FCWorkRootBase = "/var/tmp/swc-"

const unixSocketLimit = 107

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

type Bed struct {
	ID            string            `json:"bedId"`
	Slot          int               `json:"slot"`
	PID           int               `json:"pid,omitempty"`
	StartedAt     string            `json:"startedAt,omitempty"`
	Dir           string            `json:"bedDir,omitempty"`
	OrgRoot       string            `json:"orgRoot,omitempty"`
	FCWorkRoot    string            `json:"fcWorkRoot"`
	RunnerImage   string            `json:"runnerImage,omitempty"`
	RunnerImageID string            `json:"runnerImageId,omitempty"`
	HostIP        string            `json:"hostIp,omitempty"`
	Kubeconfig    string            `json:"kubeconfig,omitempty"`
	Processes     map[string]Proc   `json:"processes,omitempty"`
	Services      map[string]Proc   `json:"services,omitempty"`
	Bidders       []Bidder          `json:"bidders,omitempty"`
	Readings      map[string]any    `json:"readings,omitempty"`
	Checks        map[string]string `json:"checks,omitempty"`
}

var bedIDPattern = regexp.MustCompile(`^[a-z0-9]{1,12}$`)

func ValidBedID(id string) bool {
	return bedIDPattern.MatchString(id)
}

func FCWorkRootFor(id string) string {
	return FCWorkRootBase + id
}

func GuestSocketPath(workRoot string) string {
	return workRoot + "/fc-000000000000-00000000/fc.sock"
}

func LoadBed(path string) (Bed, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return Bed{}, err
	}
	var bed Bed
	if err := json.Unmarshal(body, &bed); err != nil {
		return Bed{}, fmt.Errorf("%s: %w", path, err)
	}
	return bed, nil
}

func (b Bed) Ports() []int {
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

func (b Bed) Validate() error {
	if !ValidBedID(b.ID) {
		return fmt.Errorf("bed id %q is not 1-12 characters of [a-z0-9]", b.ID)
	}
	if b.FCWorkRoot != FCWorkRootFor(b.ID) {
		return fmt.Errorf("bed %s keeps its guests under %s, not its own %s", b.ID, b.FCWorkRoot, FCWorkRootFor(b.ID))
	}
	if sock := GuestSocketPath(b.FCWorkRoot); len(sock) > unixSocketLimit {
		return fmt.Errorf("a guest socket of bed %s would be %d bytes, over the %d-byte limit", b.ID, len(sock), unixSocketLimit)
	}
	owner := map[int]string{}
	for _, group := range []struct {
		kind  string
		procs map[string]Proc
	}{{"process", b.Processes}, {"service", b.Services}} {
		for name, p := range group.procs {
			if group.kind == "service" && p.Port <= 0 {
				return fmt.Errorf("bed %s: service %s never reported the port it bound", b.ID, name)
			}
			if p.Port <= 0 {
				continue
			}
			if other, taken := owner[p.Port]; taken {
				return fmt.Errorf("bed %s: %s and %s both claim port %d", b.ID, other, name, p.Port)
			}
			owner[p.Port] = name
		}
	}
	return nil
}

func OverlappingPorts(a, b Bed) []int {
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
