package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func captureStdout(t *testing.T, run func() error) (string, error) {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("opening a pipe for standard output: %v", err)
	}
	saved := os.Stdout
	os.Stdout = write
	done := make(chan string, 1)
	go func() {
		raw, _ := io.ReadAll(read)
		done <- string(raw)
	}()
	runErr := run()
	os.Stdout = saved
	if err := write.Close(); err != nil {
		t.Fatalf("closing the pipe standard output was written to: %v", err)
	}
	out := <-done
	if err := read.Close(); err != nil {
		t.Fatalf("closing the pipe standard output was read from: %v", err)
	}
	return out, runErr
}

func writePreinstallManifest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "preinstall.json")
	if err := os.WriteFile(path, []byte(`{"packages":["cloud-init"]}`), 0o600); err != nil {
		t.Fatalf("writing the preinstall manifest: %v", err)
	}
	return path
}

func TestEnsureRefusesAnImageThatRecordsNoPreinstallWhenOneIsNamed(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	preinstall := writePreinstallManifest(t)

	without := cfg
	without.Preinstall = ""
	reused := writeImageUnderItsName(t, without, nameOf(t, without))

	err := Ensure(cfg.Path, true, true, preinstall, false, "")
	if err == nil {
		t.Fatalf("the image in %s records no preinstall and the preinstall manifest %s was named, "+
			"and it was handed back anyway: the manifest is digested into the image's name, so an "+
			"image built without it is not the image these inputs name, and a guest booted from it "+
			"comes up and reaches Running with none of the software the manifest exists to install",
			reused, preinstall)
	}
	if !strings.Contains(err.Error(), reused) {
		t.Errorf("the refusal does not name the image it refused (%s): %v", reused, err)
	}
	if !strings.Contains(err.Error(), preinstall) {
		t.Errorf("the refusal does not name the preinstall manifest it was asked for (%s): %v",
			preinstall, err)
	}
}

func TestEnsureRefusesAnImageThatRecordsAPreinstallWhenNoneIsNamed(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)

	withPreinstall := cfg
	withPreinstall.Preinstall = writePreinstallManifest(t)
	reused := writeImageUnderItsName(t, withPreinstall, nameOf(t, withPreinstall))

	err := Ensure(cfg.Path, true, true, "", false, "")
	if err == nil {
		t.Fatalf("the image in %s records the preinstall manifest %s and this run named none, and "+
			"it was handed back anyway: the manifest is digested into the image's name, so this "+
			"image is not the one these inputs name", reused, withPreinstall.Preinstall)
	}
	if !strings.Contains(err.Error(), reused) {
		t.Errorf("the refusal does not name the image it refused (%s): %v", reused, err)
	}
	if !strings.Contains(err.Error(), withPreinstall.Preinstall) {
		t.Errorf("the refusal does not name the preinstall manifest the image records (%s): %v",
			withPreinstall.Preinstall, err)
	}
}

func TestEnsureUsesAnImageThatRecordsThePreinstallTheInputsName(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	preinstall := writePreinstallManifest(t)

	withPreinstall := cfg
	withPreinstall.Preinstall = preinstall
	dir := writeImageUnderItsName(t, withPreinstall, nameOf(t, withPreinstall))

	if err := Ensure(cfg.Path, true, true, preinstall, false, ""); err != nil {
		t.Fatalf("the image in %s records exactly the preinstall manifest these inputs name, and it "+
			"was refused: %v", dir, err)
	}
}

func TestPresentOnlySaysAbsentAndBuildsNothing(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	preinstall := writePreinstallManifest(t)

	out, err := captureStdout(t, func() error {
		return Ensure(cfg.Path, false, true, preinstall, true, "")
	})
	if err == nil {
		t.Fatal("-present-only answered a store that holds no image at all without refusing: a run " +
			"that returned success for an absent image cannot be told from one that found it")
	}
	var result struct {
		State       string `json:"state"`
		Fingerprint string `json:"fingerprint"`
		Dir         string `json:"dir"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); err != nil {
		t.Fatalf("-present-only did not print one JSON object as its last line (%q): %v", out, err)
	}
	if result.State != stateAbsent {
		t.Errorf("a store holding no image of these inputs answered state %q, want %q", result.State, stateAbsent)
	}
	if result.Fingerprint == "" || result.Dir == "" {
		t.Errorf("the absent answer names no image (%+v), so a caller cannot tell which image it is "+
			"about to build", result)
	}
	if names := entriesIn(t, cfg.OutDir); len(names) != 0 {
		t.Fatalf("-present-only left %q in the store: it is a question, and a question that writes "+
			"is one that can be asked only where writing is allowed", names)
	}
}

func TestPresentOnlySaysPresentForTheImageTheInputsName(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	preinstall := writePreinstallManifest(t)

	withPreinstall := cfg
	withPreinstall.Preinstall = preinstall
	dir := writeImageUnderItsName(t, withPreinstall, nameOf(t, withPreinstall))

	out, err := captureStdout(t, func() error {
		return Ensure(cfg.Path, false, true, preinstall, true, "")
	})
	if err != nil {
		t.Fatalf("-present-only refused the image in %s, which is the whole build of exactly these "+
			"inputs: %v", dir, err)
	}
	var result struct {
		State string `json:"state"`
		Dir   string `json:"dir"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &result); err != nil {
		t.Fatalf("-present-only did not print one JSON object as its last line (%q): %v", out, err)
	}
	if result.State != statePresent || result.Dir != dir {
		t.Errorf("-present-only answered state %q dir %q, want state %q dir %q",
			result.State, result.Dir, statePresent, dir)
	}
}
