package cloudprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

type FileInstances struct {
	Path string
}

func NewFileInstances(path string) *FileInstances {
	return &FileInstances{Path: path}
}

func (f *FileInstances) Get(ctx context.Context, nodeName string) (Instance, bool, error) {
	if err := ctx.Err(); err != nil {
		return Instance{}, false, err
	}
	if f.Path == "" {
		return Instance{}, false, errors.New(
			"cloudprovider: the file-backed instance source has no path, so it can observe no " +
				"instance and every Node would read as unknown")
	}
	raw, err := os.ReadFile(f.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Instance{}, false, nil
		}
		return Instance{}, false, fmt.Errorf(
			"cloudprovider: reading the instance snapshot at %s: %w. A read that failed is an "+
				"UNREADABLE source and not an empty list: treating it as empty would answer "+
				"\"the guest is gone\" for every Node of a running cluster", f.Path, err)
	}
	var all []Instance
	if err := json.Unmarshal(raw, &all); err != nil {
		return Instance{}, false, fmt.Errorf(
			"cloudprovider: the instance snapshot at %s is not a JSON array of instance records: "+
				"%w. It is refused rather than read as empty, because an unparseable snapshot and "+
				"an absent instance are opposite facts", f.Path, err)
	}
	for i := range all {
		inst := all[i]
		if strings.TrimSpace(inst.NodeName) != nodeName {
			continue
		}
		inst.Exists = true
		return inst, true, nil
	}
	return Instance{}, false, nil
}
