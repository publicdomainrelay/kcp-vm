package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodemarket"
)

type claimAdopter interface {
	Adopt(claim v1alpha1.NodeClaim) error
}

func (h *nodeHosts) adopt(ctx context.Context, cluster string) error {
	if !h.opts.Substrate.deferredJoin() || h.opts.Claims == nil {
		return nil
	}
	target, err := claimClusterFor(h.opts.Registry, cluster)(ctx)
	if err != nil {
		return fmt.Errorf("clusterprovider: the home workspace of %s, which holds the claims of the "+
			"market nodes a previous provider process rented: %w", cluster, err)
	}
	claims, err := h.opts.Claims.ListNodeClaims(ctx, target.Workspace)
	if err != nil {
		return fmt.Errorf("clusterprovider: listing the node claims in %s to adopt the live ones of "+
			"%s: %w -- a read that failed is not a workspace with no claims, and the next pass reads "+
			"it again", target.Workspace, cluster, err)
	}
	return h.adoptClaims(cluster, claims)
}

func (h *nodeHosts) adoptOnce(ctx context.Context, cluster string) error {
	h.mu.Lock()
	done := h.adopted[cluster]
	h.mu.Unlock()
	if done {
		return nil
	}
	return h.adopt(ctx, cluster)
}

func (h *nodeHosts) adoptClaims(cluster string, claims []v1alpha1.NodeClaim) error {
	if !h.opts.Substrate.deferredJoin() {
		return nil
	}
	prefix := strings.ToLower(cluster) + "-node"
	var live []v1alpha1.NodeClaim
	for i := range claims {
		claim := claims[i]
		if claim.Spec.Cluster != cluster || !nodemarket.Adoptable(claim) {
			continue
		}
		if _, ok := nodeOrdinalInName(prefix, claim.Name); !ok {
			continue
		}
		live = append(live, claim)
	}
	if len(live) == 0 {
		h.markAdopted(cluster)
		return nil
	}

	h.mu.Lock()
	n, ok := h.hosts[cluster]
	if !ok {
		built, err := newNodeHost(h.opts, h.log, cluster, nodeID)
		if err != nil {
			h.mu.Unlock()
			return err
		}
		built.host, built.nodeName = nil, ""
		h.hosts[cluster] = built
		n = built
	}
	h.mu.Unlock()

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.started == 0 && n.host != nil {
		n.host, n.nodeName = nil, ""
	}
	held := map[string]bool{}
	for _, node := range n.heldNodesLocked() {
		held[node.name] = true
	}
	var problems []error
	adopted := 0
	for _, claim := range live {
		if held[claim.Name] {
			continue
		}
		ordinal, _ := nodeOrdinalInName(prefix, claim.Name)
		built, err := newNodeHost(h.opts, h.log, cluster, nodeIDFor(ordinal))
		if err != nil {
			problems = append(problems, err)
			continue
		}
		adopter, ok := built.host.(claimAdopter)
		if !ok {
			problems = append(problems, fmt.Errorf("clusterprovider: the %s substrate's node host for "+
				"%s cannot adopt a claim", h.opts.Substrate.Name, claim.Name))
			continue
		}
		if err := adopter.Adopt(claim); err != nil {
			problems = append(problems, err)
			continue
		}
		n.retain(ordinal, heldNode{name: claim.Name, host: built.host})
		n.markLive(claim.Name)
		n.started++
		adopted++
		h.log.Info("cluster provider: adopted a live market node a previous provider process rented",
			"cluster", cluster, "node", claim.Name, "phase", claim.Status.Phase,
			"reason", "the guest joined with no session of ours and outlives this process, so its "+
				"claim is held again without being rewritten: the loss, scale-in and teardown rules "+
				"and the node count treat it like a node this process rented")
	}
	if adopted > 0 {
		if err := n.writeSnapshot(); err != nil {
			problems = append(problems, err)
		}
	}
	if len(problems) == 0 {
		h.markAdopted(cluster)
	}
	return errors.Join(problems...)
}

func (h *nodeHosts) releaseStranded(ctx context.Context, cluster string, claims []v1alpha1.NodeClaim) error {
	if !h.opts.Substrate.deferredJoin() {
		return nil
	}
	prefix := strings.ToLower(cluster) + "-node"
	held := h.heldNames(cluster)
	var problems []error
	for i := range claims {
		claim := claims[i]
		if claim.Spec.Cluster != cluster || claim.DeletionTimestamp != nil ||
			claim.Status.Phase != v1alpha1.NodeClaimFailed || held[claim.Name] {
			continue
		}
		ordinal, ok := nodeOrdinalInName(prefix, claim.Name)
		if !ok {
			continue
		}
		built, err := newNodeHost(h.opts, h.log, cluster, nodeIDFor(ordinal))
		if err != nil {
			problems = append(problems, err)
			continue
		}
		h.log.Info("cluster provider: deleting a Failed claim no provider process holds",
			"cluster", cluster, "node", claim.Name, "message", claim.Status.Message,
			"reason", "the door releases a claim's machine only when the claim is deleted, and a claim "+
				"that was already Failed when this process first read it is never adopted, so nothing "+
				"else would ever delete it and its machine would run on")
		if err := built.host.Reap(ctx); err != nil {
			problems = append(problems, fmt.Errorf("clusterprovider: releasing the Failed claim %s of %s: %w",
				claim.Name, cluster, err))
		}
	}
	return errors.Join(problems...)
}

func (h *nodeHosts) markAdopted(cluster string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.adopted == nil {
		h.adopted = map[string]bool{}
	}
	h.adopted[cluster] = true
}
