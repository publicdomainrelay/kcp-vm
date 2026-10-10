package nodemarket

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodevm"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/scale"
)

type Claims interface {
	WriteNodeClaim(ctx context.Context, logicalCluster string, claim v1alpha1.NodeClaim) error

	ReadNodeClaim(ctx context.Context, logicalCluster, name string) (*v1alpha1.NodeClaim, error)

	DeleteNodeClaim(ctx context.Context, logicalCluster, name string) error
}

type Cluster struct {
	Workspace string

	OwnerDid string
}

type ClusterSource func(ctx context.Context) (Cluster, error)

type Options struct {
	Cluster string

	NodeName string

	AdvertiseAddress string

	ApiserverPort int

	TokenFile string

	ServerCAFile string

	DataDir string

	DenyBidderDids []string

	Resolve ClusterSource

	Claims Claims

	Record func(ctx context.Context, nodeName string, phase v1alpha1.NodePhase) error

	Log *slog.Logger

	ReleaseTimeout time.Duration

	Poll time.Duration
}

const DefaultReleaseTimeout = 2 * time.Minute

const DefaultPoll = 500 * time.Millisecond

const fatalReadTimeout = 2 * time.Second

var ErrIncomplete = errors.New(
	"nodemarket: a market node is a claim written into the cluster's own home workspace and " +
		"fulfilled by a door in another process, and this one is missing an input that cannot be " +
		"derived: the workspace and owner DID it is claimed under, the store the claim is written " +
		"through, the paths the door reads the join material from, the address and port the node " +
		"dials its apiserver on, or the Node CR its phase is recorded in")

type Host struct {
	mu   sync.Mutex
	opts Options
}

var _ scale.Nodes = (*Host)(nil)

func New(opts Options) (*Host, error) {
	opts = opts.withDefaults()
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	return &Host{opts: opts}, nil
}

func (o Options) withDefaults() Options {
	if o.ReleaseTimeout <= 0 {
		o.ReleaseTimeout = DefaultReleaseTimeout
	}
	if o.Poll <= 0 {
		o.Poll = DefaultPoll
	}
	return o
}

func (o Options) Validate() error {
	var missing []string
	for _, req := range []struct{ name, value string }{
		{"Cluster", o.Cluster},
		{"NodeName", o.NodeName},
		{"AdvertiseAddress", o.AdvertiseAddress},
		{"TokenFile", o.TokenFile},
		{"ServerCAFile", o.ServerCAFile},
		{"DataDir", o.DataDir},
	} {
		if req.value == "" {
			missing = append(missing, req.name)
		}
	}
	if o.ApiserverPort <= 0 || o.ApiserverPort > 65535 {
		missing = append(missing, "ApiserverPort")
	}
	if o.Resolve == nil {
		missing = append(missing, "Resolve")
	}
	if o.Claims == nil {
		missing = append(missing, "Claims")
	}
	if o.Record == nil {
		missing = append(missing, "Record")
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w (missing %s)", ErrIncomplete, strings.Join(missing, ", "))
	}
	return nil
}

func (h *Host) log() *slog.Logger {
	if h.opts.Log == nil {
		return slog.Default()
	}
	return h.opts.Log
}

func (h *Host) Provision(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.opts.Validate(); err != nil {
		return fmt.Errorf("nodemarket: node %s of %s: %w", h.opts.NodeName, h.opts.Cluster, err)
	}
	if err := os.MkdirAll(h.opts.DataDir, 0o700); err != nil {
		return fmt.Errorf("nodemarket: creating %s: %w", h.opts.DataDir, err)
	}
	passwordFile := nodevm.NodePasswordPath(h.opts.DataDir)
	if _, err := nodevm.MintOrReadNodePassword(passwordFile); err != nil {
		return fmt.Errorf("nodemarket: node %s of %s: %w -- the claim names this file as the path "+
			"the door reads the per-node password from, so a password that is not on disk is a "+
			"claim the door can only fail on", h.opts.NodeName, h.opts.Cluster, err)
	}
	target, err := h.opts.Resolve(ctx)
	if err != nil {
		return fmt.Errorf("nodemarket: the workspace and owner DID node %s of %s is claimed under: %w",
			h.opts.NodeName, h.opts.Cluster, err)
	}
	if err := h.opts.Record(ctx, h.opts.NodeName, v1alpha1.NodePending); err != nil {
		return fmt.Errorf("nodemarket: recording node %s of %s as Pending before its claim is "+
			"written: %w -- the phase is written first because the claim is about to exist, and a "+
			"gateway that died between the two would leave no record at all of a node it rented",
			h.opts.NodeName, h.opts.Cluster, err)
	}
	claim := v1alpha1.NodeClaim{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1alpha1.GroupVersion.String(),
			Kind:       "NodeClaim",
		},
		ObjectMeta: metav1.ObjectMeta{Name: h.opts.NodeName},
		Spec: v1alpha1.NodeClaimSpec{
			Cluster:          h.opts.Cluster,
			NodeName:         h.opts.NodeName,
			OwnerDid:         target.OwnerDid,
			ApiserverPort:    h.opts.ApiserverPort,
			AdvertiseAddress: h.opts.AdvertiseAddress,
			TokenFile:        h.opts.TokenFile,
			NodePasswordFile: passwordFile,
			ServerCAFile:     h.opts.ServerCAFile,
			DenyBidderDids:   h.opts.DenyBidderDids,
		},
	}
	if err := h.opts.Claims.WriteNodeClaim(ctx, target.Workspace, claim); err != nil {
		return fmt.Errorf("nodemarket: writing the claim for node %s of %s into %s: %w -- the claim "+
			"is the whole request, so nothing was rented and the next pass may ask again",
			h.opts.NodeName, h.opts.Cluster, target.Workspace, err)
	}
	h.log().Info("nodemarket: node claimed from the market",
		"cluster", h.opts.Cluster, "node", h.opts.NodeName, "workspace", target.Workspace,
		"ownerDid", target.OwnerDid, "advertiseAddress", h.opts.AdvertiseAddress,
		"apiserverPort", h.opts.ApiserverPort, "phase", v1alpha1.NodePending,
		"next", "ConfirmJoined, once the door reports the claim Running")
	return nil
}

func (h *Host) ConfirmJoined(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	claim, err := h.read(ctx)
	if err != nil {
		return err
	}
	if claim == nil {
		return fmt.Errorf("nodemarket: node %s of %s is reported by its cluster and its claim is "+
			"gone: the door releases the claim only once the machine is deleted, so a cluster still "+
			"naming this node is a node nothing here can account for",
			h.opts.NodeName, h.opts.Cluster)
	}
	switch claim.Status.Phase {
	case v1alpha1.NodeClaimRunning:
	case v1alpha1.NodeClaimFailed:
		return fmt.Errorf("nodemarket: node %s of %s: %s",
			h.opts.NodeName, h.opts.Cluster, failedMessage(claim))
	default:
		return fmt.Errorf("nodemarket: node %s of %s is reported by its cluster while its claim is "+
			"%q, not Running. The door writes Running only once the node's own agent reached its "+
			"apiserver, so a cluster reporting the node before that is a node something else "+
			"registered under this name", h.opts.NodeName, h.opts.Cluster, claim.Status.Phase)
	}
	if err := h.opts.Record(ctx, h.opts.NodeName, v1alpha1.NodeRunning); err != nil {
		return fmt.Errorf("nodemarket: recording node %s of %s as Running after the door reported "+
			"it: %w", h.opts.NodeName, h.opts.Cluster, err)
	}
	return nil
}

func (h *Host) Fatal() string {
	ctx, cancel := context.WithTimeout(context.Background(), fatalReadTimeout)
	defer cancel()
	h.mu.Lock()
	defer h.mu.Unlock()
	claim, err := h.read(ctx)
	if err != nil || claim == nil {
		return ""
	}
	if claim.Status.Phase != v1alpha1.NodeClaimFailed {
		return ""
	}
	return failedMessage(claim)
}

func (h *Host) Teardown(ctx context.Context) error {
	return h.release(ctx)
}

func (h *Host) Reap(ctx context.Context) error {
	return h.release(ctx)
}

func (h *Host) release(ctx context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.opts.Validate(); err != nil {
		return fmt.Errorf("nodemarket: node %s of %s: %w", h.opts.NodeName, h.opts.Cluster, err)
	}
	target, err := h.opts.Resolve(ctx)
	if err != nil {
		return fmt.Errorf("nodemarket: the workspace the claim for node %s of %s lives in: %w",
			h.opts.NodeName, h.opts.Cluster, err)
	}
	var problems []error
	if err := h.opts.Record(ctx, h.opts.NodeName, v1alpha1.NodeDraining); err != nil {
		problems = append(problems, fmt.Errorf("nodemarket: recording node %s of %s as Draining: %w",
			h.opts.NodeName, h.opts.Cluster, err))
	}
	h.log().Info("nodemarket: releasing the node", "cluster", h.opts.Cluster, "node", h.opts.NodeName)
	if err := h.opts.Claims.DeleteNodeClaim(ctx, target.Workspace, h.opts.NodeName); err != nil {
		problems = append(problems, fmt.Errorf("nodemarket: deleting the claim for node %s of %s: "+
			"%w -- the claim is the only handle on the rented machine, and a delete that did not "+
			"land leaves the door holding a session for a node nothing counts",
			h.opts.NodeName, h.opts.Cluster, err))
		return errors.Join(problems...)
	}
	if err := h.awaitRelease(ctx, target.Workspace); err != nil {
		problems = append(problems, err)
	}
	return errors.Join(problems...)
}

func (h *Host) awaitRelease(ctx context.Context, workspace string) error {
	deadline := time.Now().Add(h.opts.ReleaseTimeout)
	for {
		claim, err := h.opts.Claims.ReadNodeClaim(ctx, workspace, h.opts.NodeName)
		if err != nil {
			return fmt.Errorf("nodemarket: reading the claim for node %s of %s back while waiting "+
				"for the door to release it: %w -- a read that failed is not a claim that is gone, "+
				"and reporting a release nobody observed is how a machine that is still being paid "+
				"for reads as closed", h.opts.NodeName, h.opts.Cluster, err)
		}
		if claim == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("nodemarket: the claim for node %s of %s is still there %s after it "+
				"was deleted. The door removes its finalizer only once the market VM is gone, so "+
				"this is a machine that has not been destroyed: closing is not destroying, and "+
				"reporting this as a completed teardown would leave the rent running",
				h.opts.NodeName, h.opts.Cluster, h.opts.ReleaseTimeout)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("nodemarket: waiting for the door to release node %s of %s: %w",
				h.opts.NodeName, h.opts.Cluster, ctx.Err())
		case <-time.After(h.opts.Poll):
		}
	}
}

func (h *Host) read(ctx context.Context) (*v1alpha1.NodeClaim, error) {
	if err := h.opts.Validate(); err != nil {
		return nil, fmt.Errorf("nodemarket: node %s of %s: %w", h.opts.NodeName, h.opts.Cluster, err)
	}
	target, err := h.opts.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("nodemarket: the workspace the claim for node %s of %s lives in: %w",
			h.opts.NodeName, h.opts.Cluster, err)
	}
	claim, err := h.opts.Claims.ReadNodeClaim(ctx, target.Workspace, h.opts.NodeName)
	if err != nil {
		return nil, fmt.Errorf("nodemarket: reading the claim for node %s of %s from %s: %w",
			h.opts.NodeName, h.opts.Cluster, target.Workspace, err)
	}
	return claim, nil
}

func failedMessage(claim *v1alpha1.NodeClaim) string {
	if claim.Status.Message != "" {
		return claim.Status.Message
	}
	return "the market failed this claim and the door recorded no reason"
}
