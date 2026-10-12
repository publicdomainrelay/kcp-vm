package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/tsbundle"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tsbundle/tsbuild"
)

type summary struct {
	Out          string                 `json:"out"`
	Release      bool                   `json:"release"`
	TreeSHA256   string                 `json:"treeSha256"`
	ArchiveBytes int64                  `json:"archiveBytes"`
	Repos        []tsbundle.RepoRecord  `json:"repos"`
	Lockfiles    []tsbundle.LockRecord  `json:"lockfiles"`
	Entries      []tsbundle.EntryRecord `json:"entries"`
}

func main() {
	sourceRoot := flag.String("source-root", "", "the socialweb-computer-kcp checkout to bundle from; empty walks up from the working directory, then this executable's")
	entries := flag.String("entries", "", "the entries file; empty is <source-root>/deploy/ts-entries.yaml")
	deno := flag.String("deno", "", "absolute path of the pinned deno; never found on PATH")
	out := flag.String("out", "", "where closure.tar.zst and manifest.json are written; empty is internal/tsbundle/closure/<goos>_<goarch>, the release embed")
	work := flag.String("work", "", "the staging directory; empty is a temporary one removed afterwards")
	release := flag.Bool("release", false, "refuse a dirty staged path, a created or drifted lockfile, and an entry that does not start offline")
	offline := flag.Bool("offline", true, "start every entry under unshare -rn")
	epoch := flag.String("source-date-epoch", os.Getenv("SOURCE_DATE_EPOCH"), "tar mtime and cache time; empty is the commit time of the checkout's HEAD")
	allow := flag.String("allow-import", "", "comma-separated hosts deno may import from while filling, replacing its defaults")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	err := run(ctx, *sourceRoot, *entries, *deno, *out, *work, *release, *offline, *epoch, *allow)
	stop()
	if err != nil {
		fmt.Fprintf(os.Stderr, "socialweb-tsbundle: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, sourceRoot, entries, deno, out, work string, release, offline bool, epoch, allow string) error {
	var root string
	var err error
	if sourceRoot != "" {
		root, err = tsbundle.CheckCheckout(sourceRoot)
	} else {
		var starts []string
		if wd, werr := os.Getwd(); werr == nil {
			starts = append(starts, wd)
		}
		if exe, xerr := os.Executable(); xerr == nil {
			starts = append(starts, filepath.Dir(exe))
		}
		root, err = tsbundle.FindCheckout(starts...)
	}
	if err != nil {
		return err
	}
	if deno == "" {
		return errors.New("-deno is required: the absolute path of the pinned deno")
	}
	if entries == "" {
		entries = filepath.Join(root, filepath.FromSlash(tsbundle.EntriesFile))
	}
	specs, err := tsbuild.LoadEntries(entries)
	if err != nil {
		return err
	}
	if out == "" {
		out = filepath.Join(root, "internal", "tsbundle", "closure", runtime.GOOS+"_"+runtime.GOARCH)
	}
	var sde int64
	if epoch != "" {
		if sde, err = strconv.ParseInt(epoch, 10, 64); err != nil || sde <= 0 {
			return fmt.Errorf("-source-date-epoch %q is not a positive unix time", epoch)
		}
	}
	if offline {
		if err := exec.Command("unshare", "-rn", "true").Run(); err != nil {
			return fmt.Errorf("-offline needs unshare -rn (%v); pass -offline=false to start entries with the network up", err)
		}
	}
	if work == "" {
		if work, err = os.MkdirTemp("", "socialweb-tsbundle-"); err != nil {
			return err
		}
		defer removeAll(work)
	}
	var hosts []string
	if allow != "" {
		hosts = strings.Split(allow, ",")
	}
	m, err := tsbuild.Build(ctx, tsbuild.BuildOptions{Root: root, Entries: specs, Deno: deno, Release: release, Offline: offline,
		Epoch: sde, Work: work, Out: out, AllowImport: hosts, Log: os.Stderr})
	if err != nil {
		return err
	}
	b, _ := json.MarshalIndent(summary{Out: out, Release: m.Release, TreeSHA256: m.TreeSHA256, ArchiveBytes: m.ArchiveBytes,
		Repos: m.Repos, Lockfiles: m.Lockfiles, Entries: m.Entries}, "", "  ")
	fmt.Println(string(b))
	return nil
}

func removeAll(dir string) {
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			os.Chmod(p, 0o755)
		}
		return nil
	})
	os.RemoveAll(dir)
}
