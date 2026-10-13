package appctl

import (
	"context"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/diskusage"
)

const StateDirSampleEvery = 3 * time.Minute

func (ctl *Controller) sampleStateDir(ctx context.Context) {
	dir := ctl.c.Layout.StateDir
	if dir == "" {
		return
	}
	t := time.NewTicker(StateDirSampleEvery)
	defer t.Stop()
	for {
		u, err := diskusage.Of(dir)
		if err != nil {
			ctl.c.log().Warn("sampling the state dir size", "dir", dir, "err", err.Error())
		} else {
			ctl.stateDir.Store(&v1alpha1.StateDirUsage{Path: dir, Bytes: u.Bytes, Files: u.Files,
				SampledAt: time.Now().UTC().Format(time.RFC3339)})
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
