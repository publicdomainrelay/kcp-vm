package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterprovider"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodeqemu"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/noderecord"
)

type nodeIdentity func(clusterDir, nodeName string) []string

var errNoNodeIdentity = errors.New(
	"clusterprovider: no node-record identity is derivable for this substrate, and an identity is " +
		"the only thing that separates the process a record names from whatever later took over its " +
		"pid number. An identity has to be a token the process itself carries and this sweep can " +
		"compare against, so a substrate whose process carries no such token must say where its " +
		"identity comes from before its records can be swept at all; an invented one either signals " +
		"a process that is not ours or leaves ours running for ever")

func nodeIdentityFor(substrate string) (nodeIdentity, error) {
	switch substrate {
	case SubstrateFirecracker:
		return func(clusterDir, nodeName string) []string {
			return nodefirecracker.Identity(
				filepath.Join(clusterDir, clusterprovider.NodesDirName, nodeName))
		}, nil
	case SubstrateQEMU:
		return func(clusterDir, nodeName string) []string {
			return nodeqemu.Identity(
				filepath.Join(clusterDir, clusterprovider.NodesDirName, nodeName))
		}, nil
	}
	return nil, fmt.Errorf("%w (substrate %q)", errNoNodeIdentity, substrate)
}

func nodeHolderIdentityFor(substrate string) (nodeIdentity, error) {
	switch substrate {
	case SubstrateFirecracker:
		return func(clusterDir, nodeName string) []string {
			return nodefirecracker.NamespaceIdentity(
				filepath.Join(clusterDir, clusterprovider.NodesDirName, nodeName))
		}, nil
	case SubstrateQEMU:
		return func(clusterDir, nodeName string) []string {
			return nodeqemu.NamespaceIdentity(
				filepath.Join(clusterDir, clusterprovider.NodesDirName, nodeName))
		}, nil
	}
	return nil, fmt.Errorf("%w (substrate %q)", errNoNodeIdentity, substrate)
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
		reaped   int
		scanned  int
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
			reapedHere, err := p.sweepNodeRecord(ctx, clusterDir, node.Name(),
				filepath.Join(nodesDir, node.Name()))
			if err != nil {
				problems = append(problems, err)
				continue
			}
			reaped += reapedHere
		}
	}
	if err := errors.Join(problems...); err != nil {
		return err
	}
	p.log.Info("cluster provider: node record sweep", "stateDir", p.cfg.StateDir,
		"records", scanned, "reaped", reaped,
		"decision", "run before the first pass: a guest a hard kill left behind keeps naming "+
			"its node at the cluster's apiserver, and its record would make every later Provision "+
			"answer ErrAlreadyRunning, so that cluster could never provision again")
	return nil
}

func (p *Provider) sweepNodeRecord(ctx context.Context, clusterDir, nodeName, dataDir string) (int, error) {
	cluster := filepath.Base(clusterDir)
	rec, err := noderecord.Read(dataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf(
			"clusterprovider: the node record at %s, read before the first pass: %w -- it names a "+
				"pid this gateway would signal, and a record that cannot be read is not a node that "+
				"is absent; the sweep stops there rather than provisioning over a process nobody "+
				"can account for", noderecord.RecordPath(dataDir), err)
	}
	identity, err := nodeIdentityFor(rec.Substrate)
	if err != nil {
		return 0, fmt.Errorf(
			"clusterprovider: the record at %s was written by the %q substrate and this gateway is "+
				"configured for %q, and there is no identity to match it against: %w. The walk stops "+
				"rather than guessing, because the argv in a record is the argv ITS substrate gave "+
				"the process, so guessing reads a live guest as gone and a gone one as live",
			noderecord.RecordPath(dataDir), rec.Substrate, p.cfg.Substrate, err)
	}
	holderIdentity, err := nodeHolderIdentityFor(rec.Substrate)
	if err != nil {
		return 0, fmt.Errorf(
			"clusterprovider: the namespace identity for the %q substrate that wrote the record at "+
				"%s: %w -- a record is matched against the argv its own substrate gave the process, "+
				"and no other substrate's reading of it is evidence of anything",
			rec.Substrate, noderecord.RecordPath(dataDir), err)
	}
	id := identity(clusterDir, nodeName)
	holder := holderIdentity(clusterDir, nodeName)
	live, err := noderecord.LivenessOf(dataDir, id, holder)
	if err != nil {
		return 0, fmt.Errorf(
			"clusterprovider: deciding whether the process recorded for node %s at pid %d is this "+
				"node's own: %w -- nothing was signalled, because a signal aimed at a pid we could "+
				"not verify is a signal aimed at whatever now holds that number", nodeName, rec.PID, err)
	}
	switch live {
	case noderecord.LivenessNotOurs:
		p.log.Warn("cluster provider: a node record names a process that is not this node's agent",
			"cluster", cluster, "node", nodeName, "pid", rec.PID,
			"recordedIdentity", rec.Identity, "identity", id, "holderIdentity", holder,
			"record", noderecord.RecordPath(dataDir),
			"decision", "left alone and reported, never killed: this process is not ours, so "+
				"signalling it would kill whatever it really is, and the record stays where it is "+
				"because clearing it would hide the mismatch from every later pass")
		return 0, nil
	case noderecord.LivenessGone:
		p.log.Info("cluster provider: a node record outlived the process it names",
			"cluster", cluster, "node", nodeName, "pid", rec.PID,
			"decision", "the record is cleared; there is no process to stop and the next "+
				"Provision for this node writes a fresh one")
	default:
		p.log.Warn("cluster provider: a previous gateway left this node's agent running",
			"cluster", cluster, "node", nodeName, "pid", rec.PID, "identity", id,
			"decision", "reaped before the first pass: this gateway is about to become the only "+
				"owner of this node, and a guest it cannot reach would keep the Node object named "+
				"while the first pass booted a second guest under the same node name")
	}
	if err := noderecord.Reap(ctx, noderecord.ReapOptions{
		Cluster:        cluster,
		NodeName:       nodeName,
		DataDir:        dataDir,
		Identity:       id,
		HolderIdentity: holder,
		Log:            p.log,
	}); err != nil {
		return 0, fmt.Errorf(
			"clusterprovider: reaping node %s of %s before the first pass: %w -- the guest is "+
				"still up with its Node object, and its record is still on disk, so the pass that "+
				"follows would refuse to provision this node for ever",
			nodeName, cluster, err)
	}
	return 1, nil
}
