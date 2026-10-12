package hostsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type InstalledStarter interface {
	StartInstalled(ctx context.Context, name string) (UnitStatus, error)
}

func StartInstalled(ctx context.Context, m Manager, name string) (UnitStatus, error) {
	s, ok := m.(InstalledStarter)
	if !ok {
		return UnitStatus{}, fmt.Errorf("%w: the %s backend starts no installed unit", ErrUnsupported, m.Capabilities().Backend)
	}
	return s.StartInstalled(ctx, name)
}

type BootSetter interface {
	SetBoot(ctx context.Context, enable, disable []string) error
}

func SetBoot(ctx context.Context, m Manager, enable, disable []string) error {
	s, ok := m.(BootSetter)
	if !ok {
		return fmt.Errorf("%w: the %s backend starts nothing at boot", ErrUnsupported, m.Capabilities().Backend)
	}
	return s.SetBoot(ctx, enable, disable)
}

func StopUnit(ctx context.Context, m Manager, name string) error { return m.Stop(ctx, name) }

func InstalledSpecPath(dir, name string) string { return filepath.Join(dir, name+".json") }

func WriteInstalledSpec(dir string, spec UnitSpec) error {
	if err := ValidateSpec(spec); err != nil {
		return err
	}
	b, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := InstalledSpecPath(dir, spec.Name) + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, InstalledSpecPath(dir, spec.Name))
}

func ReadInstalledSpec(dir, name string) (UnitSpec, error) {
	if dir == "" {
		return UnitSpec{}, fmt.Errorf("hostsvc: %s: this proc client has no installed-unit directory", name)
	}
	b, err := os.ReadFile(InstalledSpecPath(dir, name))
	if err != nil {
		return UnitSpec{}, fmt.Errorf("hostsvc: %s is not installed: %w", name, err)
	}
	var spec UnitSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		return UnitSpec{}, err
	}
	if spec.Name != name {
		return UnitSpec{}, fmt.Errorf("hostsvc: the installed spec %s names %s", InstalledSpecPath(dir, name), spec.Name)
	}
	return spec, nil
}
