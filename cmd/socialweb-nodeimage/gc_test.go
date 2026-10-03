package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running a process to learn the pid of one that has exited: %v", err)
	}
	pid := cmd.Process.Pid
	if processAlive(pid) {
		t.Fatalf("pid %d is still alive after the process it belongs to exited, so this test cannot "+
			"tell a build that is gone from one that is running", pid)
	}
	return pid
}

func stagingDirIn(t *testing.T, cfg Config, fingerprint string, pid int) string {
	t.Helper()
	dir := filepath.Join(cfg.OutDir, "."+fingerprint+stagingName+"-"+strconv.Itoa(pid)+"-123456789")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, nodefirecracker.VMLinuxName), []byte("half a kernel\n"), 0o600); err != nil {
		t.Fatalf("writing into %s: %v", dir, err)
	}
	return dir
}

func storedImage(t *testing.T, cfg Config, extraModules ...string) string {
	t.Helper()
	one := cfg
	one.Modules = append(append([]string(nil), cfg.Modules...), extraModules...)
	fingerprint := nameOf(t, one)
	writeImageUnderItsName(t, one, fingerprint)
	return fingerprint
}

func agedImage(t *testing.T, cfg Config, built time.Time, extraModules ...string) string {
	t.Helper()
	fingerprint := storedImage(t, cfg, extraModules...)
	dir := storeDir(cfg, fingerprint)
	if err := os.Chtimes(dir, built, built); err != nil {
		t.Fatalf("dating %s: %v", dir, err)
	}
	return fingerprint
}

func survives(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

func TestAStagingDirectoryOfABuildThatIsGoneIsReapedAndOneOfABuildInFlightIsNot(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	fingerprint := nameOf(t, cfg)
	writeImageUnderItsName(t, cfg, fingerprint)

	gone := stagingDirIn(t, cfg, fingerprint, deadPID(t))
	running := stagingDirIn(t, cfg, fingerprint, os.Getpid())

	if err := MainGC(cfg.Path, nil, retainDefault); err != nil {
		t.Fatalf("reaping the store: %v", err)
	}

	if survives(t, gone) {
		t.Fatalf("%s is still in the store. Its directory names the pid of a process that has "+
			"exited, which is what a build killed by a crash, a full disk or a SIGKILL leaves "+
			"behind: it holds a fraction of an image, no name was ever renamed onto it, and it is "+
			"2.3 GB of a build that will never be finished", gone)
	}
	if !survives(t, running) {
		t.Fatalf("%s was removed while the process that staged it, pid %d, is still running. A "+
			"build writes into that directory for minutes and renames it into its name only when "+
			"it is whole, so removing it is removing the image a build that is running right now "+
			"is about to publish -- and it is the one thing a rule based on how old a directory "+
			"looks cannot avoid, because there is no age a build cannot outlive",
			running, os.Getpid())
	}
}

func TestTheNameTheConfigurationResolvesToSurvivesAGcHoweverOldItIs(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	current := nameOf(t, cfg)
	dir := writeImageUnderItsName(t, cfg, current)
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatalf("dating %s: %v", dir, err)
	}
	newer := agedImage(t, cfg, time.Now(), "xt_NFLOG")
	alsoNewer := agedImage(t, cfg, time.Now(), "xt_physdev")

	if err := MainGC(cfg.Path, nil, 0); err != nil {
		t.Fatalf("reaping the store: %v", err)
	}

	if !survives(t, storeDir(cfg, current)) {
		t.Fatalf("%s was reaped and it is the name the configuration in front of the run resolves "+
			"to. The next build over that configuration asks for exactly that name, so a store "+
			"that reaped it would build 2.3 GB again for an image it had, and a node booted from "+
			"the image under that name would find it gone", storeDir(cfg, current))
	}
	for _, other := range []string{newer, alsoNewer} {
		if survives(t, storeDir(cfg, other)) {
			t.Fatalf("%s survived a run that keeps one image and not the newest ones: the retention "+
				"count is what bounds the store, and a rule that kept every image would never reap "+
				"anything", storeDir(cfg, other))
		}
	}
}

func TestAnImageANodeBootedFromSurvivesAGcAndTheNewestAreKeptUpToTheCount(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	current := nameOf(t, cfg)
	writeImageUnderItsName(t, cfg, current)

	booted := agedImage(t, cfg, time.Now().Add(-48*time.Hour), "xt_NFLOG")
	middle := agedImage(t, cfg, time.Now().Add(-2*time.Hour), "xt_physdev")
	newest := agedImage(t, cfg, time.Now(), "xt_string")

	if err := MainGC(cfg.Path, []string{storeDir(cfg, booted)}, 1); err != nil {
		t.Fatalf("reaping the store: %v", err)
	}

	if !survives(t, storeDir(cfg, booted)) {
		t.Fatalf("%s was reaped although a node was said to have booted from it. Nothing this "+
			"command can see says which images a running node is running from, so -keep is the "+
			"only thing that does: reaping it takes the image out from under a node that is up, "+
			"and the next boot of that node is the first thing that says so", storeDir(cfg, booted))
	}
	if !survives(t, storeDir(cfg, newest)) {
		t.Fatalf("%s was reaped and it is the newest image in the store, so a rebuild cannot be "+
			"walked back to it", storeDir(cfg, newest))
	}
	if survives(t, storeDir(cfg, middle)) {
		t.Fatalf("%s survived a run that keeps the name the configuration resolves to, the image "+
			"a node booted from, and one more image, and it is the oldest of the rest: the count "+
			"is what decides between the images nothing protects", storeDir(cfg, middle))
	}
}

func TestAGcLeavesADirectoryThatIsNotShapedLikeThisStore(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	writeImageUnderItsName(t, cfg, nameOf(t, cfg))

	foreign := []string{
		filepath.Join(cfg.OutDir, "image"),
		filepath.Join(cfg.OutDir, "scratch"),
		filepath.Join(cfg.OutDir, ".sha256-notahexname"+stagingName+"-1-2"),
		filepath.Join(cfg.OutDir, ".building-99999999-1"),
	}
	for _, path := range foreign {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("creating %s: %v", path, err)
		}
		if err := os.WriteFile(filepath.Join(path, "something"), []byte("not this builder's\n"), 0o600); err != nil {
			t.Fatalf("writing into %s: %v", path, err)
		}
	}
	for _, name := range []string{nodefirecracker.ManifestName, nodefirecracker.RootfsImageName} {
		if err := os.WriteFile(filepath.Join(cfg.OutDir, name), []byte("not this builder's\n"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
		foreign = append(foreign, filepath.Join(cfg.OutDir, name))
	}

	plan, err := reapStore(cfg, map[string]string{nameOf(t, cfg): "the configuration resolves to it"}, 1)
	if err != nil {
		t.Fatalf("reaping the store: %v", err)
	}

	for _, path := range foreign {
		if !survives(t, path) {
			t.Fatalf("%s was removed and it is not named like anything this store writes: an image "+
				"is named %s<64 hex digits> and a build in progress is named .%s<64 hex digits>%s"+
				"-<pid>-<random>. A store that reaps whatever it does not recognise is one that "+
				"deletes what someone else put beside it, and the name it reaped this on is not a "+
				"name this builder hands out", path, nodefirecracker.FingerprintPrefix,
				nodefirecracker.FingerprintPrefix, stagingName)
		}
	}
	if len(plan.refused) != len(foreign) {
		t.Fatalf("the run removed nothing but said nothing about %d of the %d entries it left: an "+
			"operator reading the output cannot tell 'this is not mine' from 'I did not see it'",
			len(foreign)-len(plan.refused), len(foreign))
	}
	for _, entry := range plan.refused {
		if !strings.Contains(entry.reason, "not named like anything this store writes") {
			t.Fatalf("the run left %s alone and its reason is %q", entry.path, entry.reason)
		}
	}
}

func TestAGcThatCannotTellWhatTheConfigurationResolvesToReapsNothing(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	current := nameOf(t, cfg)
	writeImageUnderItsName(t, cfg, current)
	stale := stagingDirIn(t, cfg, current, deadPID(t))
	other := agedImage(t, cfg, time.Now(), "xt_NFLOG")

	if err := os.Remove(cfg.BaseImage); err != nil {
		t.Fatalf("removing the base image: %v", err)
	}

	err := MainGC(cfg.Path, nil, 1)
	if err == nil {
		t.Fatal("a run whose base image cannot be read reaped the store, so it chose which images " +
			"to keep without knowing the one name it always keeps")
	}
	if !strings.Contains(err.Error(), "cannot tell") {
		t.Fatalf("the refusal does not say that it cannot tell which image the configuration "+
			"resolves to: %v", err)
	}
	for _, path := range []string{stale, storeDir(cfg, other), storeDir(cfg, current)} {
		if !survives(t, path) {
			t.Fatalf("%s was removed by a run that could not name the image the configuration "+
				"resolves to. The one name this rule always keeps is that one, so a run that cannot "+
				"work out the name cannot tell what to keep, and reaping on an unknown is how a "+
				"store loses the image a running node booted from", path)
		}
	}
}

func TestAnImageThatIsNotWholeIsReapedAndAWholeOneIsNot(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	current := nameOf(t, cfg)
	writeImageUnderItsName(t, cfg, current)

	half := storedImage(t, cfg, "xt_NFLOG")
	if err := os.Remove(filepath.Join(storeDir(cfg, half), nodefirecracker.RootfsImageName)); err != nil {
		t.Fatalf("removing the root filesystem of %s: %v", storeDir(cfg, half), err)
	}

	plan, err := reapStore(cfg, map[string]string{current: "the configuration resolves to it"}, retainDefault)
	if err != nil {
		t.Fatalf("reaping the store: %v", err)
	}
	if survives(t, storeDir(cfg, half)) {
		t.Fatalf("%s carries the name of an image and does not hold a whole build of one, and it "+
			"was left where it is. A build publishes by renaming a finished directory into that "+
			"name, so a directory under it was not written by a build that ran to the end, and the "+
			"next build over that name refuses to publish over it and asks the operator to remove "+
			"it by hand", storeDir(cfg, half))
	}
	if !survives(t, storeDir(cfg, current)) {
		t.Fatalf("%s was reaped and the configuration resolves to it", storeDir(cfg, current))
	}
	found := false
	for _, entry := range plan.reaped {
		if entry.fingerprint == half && strings.Contains(entry.reason, "does not hold a whole build") {
			found = true
		}
	}
	if !found {
		t.Fatal("the run removed a directory that is not a whole image and did not say that was " +
			"the reason, so the operator cannot tell it apart from an image reaped for being old")
	}
}

func TestAKeepThatNamesSomethingThisStoreCannotHoldIsRefused(t *testing.T) {
	cfg := imageFixture(t)
	writeConfigFile(t, cfg)
	writeImageUnderItsName(t, cfg, nameOf(t, cfg))

	err := MainGC(cfg.Path, []string{"a-node-image-i-remember"}, retainDefault)
	if err == nil {
		t.Fatal("-keep accepted a name that is not the fingerprint of anything, so the image the " +
			"operator meant to protect was not protected and the run went on to reap")
	}
	if !strings.Contains(err.Error(), nodefirecracker.FingerprintPrefix) {
		t.Fatalf("the refusal does not say what a name in this store looks like: %v", err)
	}

	absent := nodefirecracker.FingerprintPrefix + strings.Repeat("0", 64)
	err = MainGC(cfg.Path, []string{absent}, retainDefault)
	if err == nil {
		t.Fatal("-keep accepted the name of an image that is not in the store, so a mistyped name " +
			"protects nothing and nothing says so")
	}
	if !strings.Contains(err.Error(), absent) {
		t.Fatalf("the refusal does not name the image it could not find: %v", err)
	}
}
