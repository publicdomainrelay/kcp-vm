package apigen

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

const RevisionLockFile = "deploy/apiresourceschema-revisions.txt"

type Output struct {
	Path string
	Data []byte
}

func Generate(root string) ([]Output, error) {
	m, err := BuildV1alpha1(root)
	if err != nil {
		return nil, err
	}
	lockData, err := os.ReadFile(filepath.Join(root, RevisionLockFile))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	lock, err := ParseRevisionLock(lockData)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", RevisionLockFile, err)
	}

	var out []Output
	for _, r := range m.Resources {
		data, err := m.APIResourceSchema(r)
		if err != nil {
			return nil, err
		}
		name, hash, err := SpecHash(data)
		if err != nil {
			return nil, err
		}
		if err := lock.Admit(name, hash); err != nil {
			return nil, err
		}
		out = append(out, Output{Path: SchemaFile(r), Data: data})
	}
	for _, e := range m.Exports() {
		data, err := m.APIExport(e)
		if err != nil {
			return nil, err
		}
		out = append(out, Output{Path: ExportFile(e), Data: data})
	}
	lockOut, err := lock.Marshal()
	if err != nil {
		return nil, err
	}
	out = append(out, Output{Path: RevisionLockFile, Data: lockOut})
	for _, target := range TSTargets {
		generated, err := generateTSTarget(root, m, target)
		if err != nil {
			return nil, err
		}
		out = append(out, generated...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func generateTSTarget(root string, m *Model, target TSTarget) ([]Output, error) {
	sub, err := m.Subset(target.Exports)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", target.Dir, err)
	}
	ts, err := sub.TypeScript()
	if err != nil {
		return nil, err
	}
	out := []Output{{Path: target.Dir + "/mod.ts", Data: ts}}
	if target.Fixtures == "" {
		return out, nil
	}
	entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(target.Fixtures)))
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(target.Fixtures), e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, Output{Path: target.FixturesDir + "/" + e.Name(), Data: data})
	}
	return out, nil
}

func Write(dir string, outputs []Output) error {
	for _, o := range outputs {
		path := filepath.Join(dir, filepath.FromSlash(o.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, o.Data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
