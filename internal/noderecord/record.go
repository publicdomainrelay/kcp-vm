package noderecord

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const RecordFileName = "node.json"

var ErrNoIdentity = errors.New(
	"noderecord: a record needs the identity its substrate gives the process it names, and an empty " +
		"one is not a weaker record but an unusable one -- a record that carries no identity can " +
		"never be matched to a process, so every gateway that reads it answers \"not ours\" and the " +
		"process it names runs on for ever holding whatever the node held: the guest stays up and " +
		"keeps naming its node at the cluster's apiserver, and the only thing that " +
		"would have ended it is a reap that refuses to touch an identity it cannot check")

var ErrIncomplete = errors.New(
	"noderecord: reaping needs the node's name, the data dir its record sits in, and the identity " +
		"its substrate gives the process the record names; the data dir is what locates the record, " +
		"and the identity is the only thing that separates the process behind it from whatever later " +
		"took over its pid number")

var ErrNotOurs = errors.New(
	"noderecord: the process this node's record names does not carry this node's identity, so it is " +
		"not ours to stop. It is also not ours to forget: the record is LEFT WHERE IT IS, because " +
		"clearing it would take away the only thing that names the process holding this node's guest " +
		"and its Node object, and every later pass would then read an absent record, answer " +
		"\"nothing is running\", start a second process under the same node name, and find two " +
		"candidates for one identity for as long as the first one lives")

type Record struct {
	PID       int      `json:"pid"`
	StartTime uint64   `json:"startTime"`
	Identity  []string `json:"identity"`
}

func RecordPath(dataDir string) string {
	return filepath.Join(dataDir, RecordFileName)
}

func Matches(argv, want []string) bool {
	if len(want) == 0 {
		return false
	}
	for _, w := range want {
		if !holds(argv, w) {
			return false
		}
	}
	return true
}

func holds(argv []string, token string) bool {
	for _, a := range argv {
		if a == token {
			return true
		}
	}
	return false
}

func Write(dataDir string, rec Record) error {
	if len(rec.Identity) == 0 {
		return fmt.Errorf("%w (the record at %s would name pid %d)",
			ErrNoIdentity, RecordPath(dataDir), rec.PID)
	}
	path := RecordPath(dataDir)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("noderecord: creating %s for the node record: %w", dataDir, err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("noderecord: encoding the record for pid %d with identity %v: %w", rec.PID, rec.Identity, err)
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("noderecord: writing the node record at %s: %w", tmp, err)
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("noderecord: writing the node record at %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("noderecord: writing the node record at %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("noderecord: writing the node record at %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("noderecord: writing the node record at %s: %w", path, err)
	}
	return nil
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

func Remove(dataDir string) error {
	path := RecordPath(dataDir)
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("noderecord: removing the node record at %s: %w", path, err)
	}
	return nil
}
