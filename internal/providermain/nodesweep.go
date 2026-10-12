package providermain

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/noderecord"
)

var errUnverifiableNodeRecord = errors.New(
	"clusterprovider: the node record names a substrate this provider no longer places nodes with. " +
		"The local firecracker and qemu substrates were retired (plan 0005 M1-MARKET-ONLY): a market " +
		"node is a VM rented from the compute market, born from a bidder's cloud-init, and nothing " +
		"about it runs on this host, so the market path writes no node record at all -- a rented node " +
		"is a NodeClaim, and the door releases it. A record is therefore a leftover from a host that " +
		"still ran a local substrate, and its identity (the argv that substrate gave the process) is " +
		"one this provider cannot reconstruct. The record is left where it is and nothing is " +
		"signalled on it: a signal aimed at a pid nobody can verify is a signal aimed at whatever " +
		"now holds that number")

func unverifiableNodeRecord(substrate string) error {
	return fmt.Errorf("%w (substrate %q)", errUnverifiableNodeRecord, substrate)
}

func (p *Provider) sweepNodeRecords(ctx context.Context) error {
	clusters, err := os.ReadDir(p.cfg.StateDir)
	if errors.Is(err, fs.ErrNotExist) {
		p.log.Info("cluster provider: node record sweep", "stateDir", p.cfg.StateDir,
			"decision", "the state dir does not exist yet, so this gateway is the first on this "+
				"host and no earlier one left a node behind")
		return nil
	}
	if err != nil {
		return fmt.Errorf(
			"clusterprovider: reading %s before the first pass: %w -- this is the walk that finds "+
				"the node records an earlier gateway left, and a failed read is not an empty state "+
				"dir: treating the two alike would leave every one of those agents running while "+
				"the first pass provisions a second agent under the same node names",
			p.cfg.StateDir, err)
	}

	var (
		problems []error
		scanned  int
		left     int
	)
	for _, cluster := range clusters {
		if !cluster.IsDir() {
			continue
		}
		clusterDir := filepath.Join(p.cfg.StateDir, cluster.Name())
		nodesDir := clusterprovider.NodesDir(p.cfg.StateDir, cluster.Name())
		nodes, err := os.ReadDir(nodesDir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			problems = append(problems, fmt.Errorf(
				"clusterprovider: reading %s: %w -- a node record under it cannot be checked, and "+
					"a record nobody checked is one nothing may be signalled on and nothing may "+
					"provision over", nodesDir, err))
			continue
		}
		for _, node := range nodes {
			if !node.IsDir() {
				continue
			}
			scanned++
			reported, err := p.sweepNodeRecord(clusterDir, node.Name(),
				filepath.Join(nodesDir, node.Name()))
			if err != nil {
				problems = append(problems, err)
				continue
			}
			if reported {
				left++
			}
		}
	}
	if err := errors.Join(problems...); err != nil {
		return err
	}
	p.log.Info("cluster provider: node record sweep", "stateDir", p.cfg.StateDir,
		"records", scanned, "leftUnverifiable", left,
		"decision", "run before the first pass: a record under this state dir names a process by "+
			"the argv its substrate gave it, and the only placement this provider has left -- a "+
			"claim on the compute market -- starts no process here at all, so every record it finds "+
			"is one it reports rather than signals on")
	return nil
}

func (p *Provider) sweepNodeRecord(clusterDir, nodeName, dataDir string) (bool, error) {
	cluster := filepath.Base(clusterDir)
	rec, err := noderecord.Read(dataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf(
			"clusterprovider: the node record at %s, read before the first pass: %w -- it names a "+
				"pid this gateway would signal, and a record that cannot be read is not a node that "+
				"is absent; the sweep stops there rather than provisioning over a process nobody "+
				"can account for", noderecord.RecordPath(dataDir), err)
	}
	p.log.Warn("cluster provider: a node record names a process this provider cannot verify",
		"cluster", cluster, "node", nodeName, "pid", rec.PID, "substrate", rec.Substrate,
		"record", noderecord.RecordPath(dataDir),
		"error", unverifiableNodeRecord(rec.Substrate).Error(),
		"decision", "left alone and reported, never signalled: the identity in the record is the "+
			"argv ITS substrate gave the process, and this provider has no way to reconstruct it "+
			"for a placement that starts no process here")
	return true, nil
}
