package execrunner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

const reapPoll = 50 * time.Millisecond

type Reaped struct {
	Run    string `json:"run"`
	PID    int    `json:"pid"`
	Killed bool   `json:"killed"`
}

func liveRuns(runsDir string) map[string]runState {
	out := map[string]runState{}
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(runsDir, e.Name())
		body, err := os.ReadFile(filepath.Join(dir, "state.json"))
		if err != nil {
			continue
		}
		var st runState
		if json.Unmarshal(body, &st) != nil || st.Stopped || st.PID <= 0 {
			continue
		}
		if !processAlive(st.PID) || !ownedByUs(st.PID, st.Ticks) {
			continue
		}
		out[e.Name()] = st
	}
	return out
}

func ReapRuns(runsDir string, grace time.Duration) []Reaped {
	live := liveRuns(runsDir)
	if len(live) == 0 {
		return nil
	}
	for _, st := range live {
		host.termGroup(st.PID)
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		alive := false
		for _, st := range live {
			if processAlive(st.PID) && ownedByUs(st.PID, st.Ticks) {
				alive = true
				break
			}
		}
		if !alive {
			break
		}
		time.Sleep(reapPoll)
	}
	var out []Reaped
	for id, st := range live {
		r := Reaped{Run: id, PID: st.PID}
		if processAlive(st.PID) && ownedByUs(st.PID, st.Ticks) {
			host.killGroup(st.PID)
			r.Killed = true
		}
		out = append(out, r)
	}
	for time.Now().Before(deadline.Add(grace)) {
		if len(liveRuns(runsDir)) == 0 {
			break
		}
		time.Sleep(reapPoll)
	}
	return out
}
