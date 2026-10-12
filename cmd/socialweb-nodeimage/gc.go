package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

const retainDefault = 3

type entryClass int

const (
	classImage entryClass = iota

	classStaging

	classForeign
)

type storeEntry struct {
	name string

	path string

	class entryClass

	fingerprint string

	pid int

	built time.Time

	reason string
}

type reapResult struct {
	reaped []storeEntry

	kept []storeEntry

	refused []storeEntry
}

func (r reapResult) String() string {
	lines := make([]string, 0, len(r.reaped)+len(r.kept)+len(r.refused))
	for _, e := range r.reaped {
		lines = append(lines, "reaped "+e.path+" ("+e.reason+")")
	}
	for _, e := range r.kept {
		lines = append(lines, "kept "+e.path+" ("+e.reason+")")
	}
	for _, e := range r.refused {
		lines = append(lines, "left alone "+e.path+" ("+e.reason+")")
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func classify(name, path string) storeEntry {
	entry := storeEntry{name: name, path: path}
	if nodefirecracker.IsFingerprint(name) {
		entry.class = classImage
		entry.fingerprint = name
		return entry
	}
	fingerprint, pid, ok := stagingOf(name)
	if !ok {
		entry.class = classForeign
		return entry
	}
	entry.class = classStaging
	entry.fingerprint = fingerprint
	entry.pid = pid
	return entry
}

func scanStore(cfg Config) ([]storeEntry, error) {
	entries, err := os.ReadDir(cfg.OutDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("socialweb-nodeimage: reading the image store at %s: %w -- a store "+
			"that cannot be listed is one no retention rule can be applied to, and applying one to "+
			"part of what is there would be choosing which images survive without having seen the "+
			"rest", cfg.OutDir, err)
	}
	found := make([]storeEntry, 0, len(entries))
	for _, e := range entries {
		path := filepath.Join(cfg.OutDir, e.Name())
		entry := classify(e.Name(), path)
		if st, statErr := os.Stat(path); statErr == nil {
			entry.built = st.ModTime()
		}
		found = append(found, entry)
	}
	return found, nil
}

func stagingReason(entry storeEntry) string {
	if processAlive(entry.pid) {
		return "a build running as pid " + strconv.Itoa(entry.pid) + " is writing it, and a " +
			"staging directory is reaped on whether its process is gone and not on how old it looks, " +
			"because a build that outlives any age this could pick is a build whose image is deleted " +
			"out from under it"
	}
	return "no build holds it: the process that staged it as pid " + strconv.Itoa(entry.pid) +
		" is gone, so this is a build that was killed before it was renamed into a name, and what " +
		"it holds is a fraction of an image that nothing can be booted from"
}

func foreignReason() string {
	return "it is not named like anything this store writes: an image is named " +
		nodefirecracker.FingerprintPrefix + "<64 hex digits>, and a build in progress is named " +
		"." + nodefirecracker.FingerprintPrefix + "<64 hex digits>" + stagingName +
		"-<pid>-<random>. Whatever this is, it is not this builder's, so this will not remove " +
		"it -- and it will not guess, because a directory reaped on a guess is one someone put " +
		"there that nothing said was garbage"
}

func notWholeReason() string {
	return "it carries the name of an image and does not hold a whole build of one. A build " +
		"publishes by renaming a finished directory into the name its inputs give it, so a " +
		"directory under that name that is not whole was not written by a build that ran to the " +
		"end, and nothing can be booted from it"
}

func planReap(cfg Config, entries []storeEntry, protected map[string]string, retain int) (reapResult, error) {
	result := reapResult{}
	images := make([]storeEntry, 0, len(entries))
	for _, entry := range entries {
		switch entry.class {
		case classStaging:
			entry.reason = stagingReason(entry)
			if processAlive(entry.pid) {
				result.kept = append(result.kept, entry)
				continue
			}
			result.reaped = append(result.reaped, entry)
		case classForeign:
			entry.reason = foreignReason()
			result.refused = append(result.refused, entry)
		case classImage:
			images = append(images, entry)
		}
	}
	sort.Slice(images, func(i, j int) bool {
		if images[i].built.Equal(images[j].built) {
			return images[i].name > images[j].name
		}
		return images[i].built.After(images[j].built)
	})

	waiting := make([]storeEntry, 0, len(images))
	for _, entry := range images {
		whole := true
		if _, err := completeImage(cfg, entry.fingerprint); err != nil {
			var notWhole *NotWholeError
			if !errors.As(err, &notWhole) {
				return reapResult{}, err
			}
			whole = false
		}
		if reason, named := protected[entry.fingerprint]; named {
			entry.reason = reason
			if !whole {
				entry.reason += ", and it does not hold a whole build of one: that is the name the " +
					"next build will refuse to publish over, so removing it is a decision for the " +
					"operator and not for this"
			}
			result.kept = append(result.kept, entry)
			continue
		}
		if !whole {
			entry.reason = notWholeReason()
			result.reaped = append(result.reaped, entry)
			continue
		}
		waiting = append(waiting, entry)
	}

	room := retain
	for _, entry := range waiting {
		if room > 0 {
			entry.reason = "it is one of the " + strconv.Itoa(retain) + " newest images this store " +
				"keeps besides the ones a name protects, so a rebuild can be walked back to it"
			result.kept = append(result.kept, entry)
			room--
			continue
		}
		entry.reason = "nothing names it: the configuration in front of this run resolves to " +
			"another name, no -keep named it, and it is not among the " + strconv.Itoa(retain) +
			" newest images this store keeps"
		result.reaped = append(result.reaped, entry)
	}
	return result, nil
}

func reapStore(cfg Config, protected map[string]string, retain int) (reapResult, error) {
	entries, err := scanStore(cfg)
	if err != nil {
		return reapResult{}, err
	}
	plan, err := planReap(cfg, entries, protected, retain)
	if err != nil {
		return reapResult{}, err
	}
	for _, entry := range plan.reaped {
		if err := os.RemoveAll(entry.path); err != nil {
			return reapResult{}, fmt.Errorf("socialweb-nodeimage: removing %s: %w -- the image "+
				"store is what a substrate boots from, and a directory that is half removed still "+
				"answers to a name nothing can use while the next build over that name refuses to "+
				"publish over it", entry.path, err)
		}
	}
	return plan, nil
}

func MainGC(configPath string, keep []string, retain int) error {
	if configPath == "" {
		return fmt.Errorf("-config is required: the retention rule always keeps the image the " +
			"configuration in front of it resolves to, and nothing here can say which name that is " +
			"without the configuration")
	}
	if retain < 0 {
		return fmt.Errorf("-retain is %d. A store that keeps a negative number of images is not a "+
			"retention rule this can apply: it would reap every image but the one the configuration "+
			"resolves to, and a node that booted from one of the others would lose the image it is "+
			"running from", retain)
	}
	if err := refuseGuestImageConfig(configPath, "-gc"); err != nil {
		return err
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	fingerprint, err := nodefirecracker.Fingerprint(cfg.Path, buildConfigOf(cfg))
	if err != nil {
		return fmt.Errorf("socialweb-nodeimage: cannot tell which image under %s the configuration "+
			"at %s resolves to, and nothing has been reaped: the one name this rule always keeps is "+
			"that one, so a reap that ran without it is a reap that can remove the image the next "+
			"build is about to ask for: %w", cfg.OutDir, cfg.Path, err)
	}
	protected := map[string]string{fingerprint: "the configuration in front of this run resolves " +
		"to this name, and the name a configuration resolves to is kept however old it is and " +
		"however many newer images are beside it"}
	for _, kept := range keep {
		name := strings.TrimSpace(kept)
		if name == "" {
			continue
		}
		entry := filepath.Base(filepath.Clean(name))
		if !nodefirecracker.IsFingerprint(entry) {
			return fmt.Errorf("socialweb-nodeimage: -keep %s does not name an image this store "+
				"holds. What a store holds is the fingerprint of the inputs an image was built from "+
				"-- the name of its directory under %s -- so the image a running node booted from is "+
				"protected by naming that directory, e.g. -keep %s", name, cfg.OutDir,
				nodefirecracker.FingerprintPrefix+"<64 hex digits>")
		}
		if _, err := os.Stat(filepath.Join(cfg.OutDir, entry)); err != nil {
			return fmt.Errorf("socialweb-nodeimage: -keep %s names an image that is not in %s: %w "+
				"-- a name that is absent is not an error in itself, and it is refused because it "+
				"is the one mistake here that nothing else catches: a node booted from an image "+
				"whose name was mistyped goes on running while the store no longer holds what it "+
				"booted from, and the next boot of that node is the first thing that says so",
				name, cfg.OutDir, err)
		}
		protected[entry] = "-keep named it, because the command cannot see which images a running " +
			"node booted from and this is how that is said"
	}
	result, err := reapStore(cfg, protected, retain)
	if err != nil {
		return err
	}
	fmt.Printf("kept %s\n  config   %s\n  retain   %d\n",
		filepath.Join(cfg.OutDir, fingerprint), cfg.Path, retain)
	if lines := result.String(); lines != "" {
		for _, line := range strings.Split(lines, "\n") {
			fmt.Println("  " + line)
		}
	}
	return nil
}
