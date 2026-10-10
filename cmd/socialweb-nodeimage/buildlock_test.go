package main

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestASecondBuildHoldingTheSameStageDirectoryIsRefused(t *testing.T) {
	cfg := imageFixture(t)
	inside := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- withStageDirHeld(cfg, func() error {
			close(inside)
			time.Sleep(100 * time.Millisecond)
			return nil
		})
	}()
	<-inside

	second := withStageDirHeld(cfg, func() error {
		t.Error("the second build ran while the first held the stage directory, and the two of them " +
			"would have unsquashed the base image into one tree at once")
		return nil
	})
	if second == nil {
		t.Fatal("two builds were given the same stage directory at the same time, and a build " +
			"unsquashes the base image into that directory and chroots into it in place")
	}
	if !strings.Contains(second.Error(), cfg.StageDir) {
		t.Errorf("the refusal does not name the stage directory it is about: %v", second)
	}
	if !strings.Contains(second.Error(), ".lock") {
		t.Errorf("the refusal does not name the lock, so it does not say what to remove or wait "+
			"for: %v", second)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("the first build failed: %v", err)
	}

	if err := withStageDirHeld(cfg, func() error { return nil }); err != nil {
		t.Fatalf("the stage directory was still held after the build that held it returned: %v", err)
	}
}

func TestTheHolderIsNamedInTheRefusal(t *testing.T) {
	cfg := imageFixture(t)
	inside := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_ = withStageDirHeld(cfg, func() error {
			close(inside)
			<-release
			return nil
		})
	}()
	<-inside
	err := withStageDirHeld(cfg, func() error { return nil })
	close(release)
	if err == nil {
		t.Fatal("the stage directory was not held, so nothing was refused")
	}
	raw, readErr := os.ReadFile(cfg.StageDir + ".lock")
	if readErr != nil {
		t.Fatalf("reading the lock the refusal names: %v", readErr)
	}
	holder := strings.TrimSpace(string(raw))
	if holder == "" {
		t.Fatal("the lock file records no holder, so an operator reading the refusal cannot tell " +
			"which process to look for")
	}
	if !strings.Contains(err.Error(), holder) {
		t.Errorf("the refusal does not name the process holding the lock (%s): %v", holder, err)
	}
}
