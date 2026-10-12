package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clicfg"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nsexec"
)

const rollReportPoll = time.Second

func runNsExec(ctx context.Context, a *app, vals clicfg.Values, rest []string) int {
	binds, err := nsexec.ParseBinds(vals.String("bind"))
	if err != nil {
		return a.fail(exitRefused, "internal ns-exec: %v", err)
	}
	c := nsexec.Config{Hostname: vals.String("private-hostname"), Binds: binds, Argv: rest}
	if vals.Bool("inside") {
		err = nsexec.Inside(c)
	} else {
		var exe string
		if exe, err = os.Executable(); err == nil {
			err = nsexec.Enter(c, []string{exe, "internal", "ns-exec"})
		}
	}
	return a.fail(exitFailed, "internal ns-exec: %v", err)
}

func rollID() string {
	b := make([]byte, 4)
	rand.Read(b)
	return time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b)
}

func runUnitsRoll(kind string) verbFunc {
	verb := "units roll " + kind
	return func(ctx context.Context, a *app, vals clicfg.Values, rest []string) int {
		if len(rest) != 1 {
			return a.fail(exitRefused, "%s: name exactly one cluster key (the <account>-<cluster> the provider knows it by), got %v", verb, rest)
		}
		key := rest[0]
		state := vals.String("cluster-state-dir")
		if state == "" || !filepath.IsAbs(state) {
			return a.fail(exitRefused, "%s: --cluster-state-dir must name the cluster provider's absolute state dir", verb)
		}
		wait, err := time.ParseDuration(vals.String("ready-timeout"))
		if err != nil {
			return a.fail(exitRefused, "%s: ready-timeout: %v", verb, err)
		}
		req := clusterprovider.RollRequest{ID: rollID(), Kind: kind, RequestedAt: time.Now().UTC()}
		switch kind {
		case clusterprovider.RollKindCluster:
			req.K3s, req.Kine, req.IdleBound = vals.String("k3s-path"), vals.String("kine-path"), vals.String("idle-bound")
			if _, err := time.ParseDuration(req.IdleBound); err != nil {
				return a.fail(exitRefused, "%s: idle-bound: %v", verb, err)
			}
		case clusterprovider.RollKindLink:
			req.Service, req.Dumbpipe = vals.String("service"), vals.String("dumbpipe-path")
			if req.Service == "" {
				return a.fail(exitRefused, "%s: --service names the link to replace (apiserver or openbao)", verb)
			}
		}
		for _, p := range []string{req.K3s, req.Kine, req.Dumbpipe} {
			if p != "" && !filepath.IsAbs(p) {
				return a.fail(exitRefused, "%s: %s must be an absolute path", verb, p)
			}
		}
		path, err := clusterprovider.WriteRollRequest(state, key, req)
		if err != nil {
			return a.fail(exitFailed, "%s: %v", verb, err)
		}
		a.log.Info("roll requested; the cluster provider carries it out", "cluster", key, "roll", req.ID, "request", path)
		deadline := time.Now().Add(wait)
		for time.Now().Before(deadline) {
			rep, ok, err := clusterprovider.ReadRollReport(state, key, req.ID)
			if err != nil {
				return a.fail(exitFailed, "%s: %v", verb, err)
			}
			if ok {
				a.printJSON(rep)
				if rep.Error != "" {
					return exitFailed
				}
				return exitOK
			}
			select {
			case <-ctx.Done():
				return a.fail(exitFailed, "%s: interrupted; the request at %s stays for the provider", verb, path)
			case <-time.After(rollReportPoll):
			}
		}
		return a.fail(exitFailed, "%s: no report for %s after %s; the request at %s stays for the provider", verb, req.ID, wait, path)
	}
}
