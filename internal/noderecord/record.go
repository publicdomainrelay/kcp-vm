package noderecord

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const RecordFileName = "node.json"

type Record struct {
	PID       int      `json:"pid"`
	StartTime uint64   `json:"startTime"`
	Identity  []string `json:"identity"`
	Substrate string   `json:"substrate"`
}

func RecordPath(dataDir string) string {
	return filepath.Join(dataDir, RecordFileName)
}

func Read(dataDir string) (Record, error) {
	path := RecordPath(dataDir)
	b, err := os.ReadFile(path)
	if err != nil {
		return Record{}, fmt.Errorf("noderecord: reading the node record at %s: %w", path, err)
	}
	var rec Record
	if err := json.Unmarshal(b, &rec); err != nil {
		return Record{}, fmt.Errorf("noderecord: the record at %s is not readable as a node record, so which pid this node's process is cannot be known from it and nothing may be signalled on its behalf: %w", path, err)
	}
	if rec.PID <= 0 {
		return Record{}, fmt.Errorf("noderecord: the record at %s names pid %d, and a record without a pid cannot be matched against a live process", path, rec.PID)
	}
	return rec, nil
}
