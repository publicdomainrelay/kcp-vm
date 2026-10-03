package revocation

import (
	"errors"
	"fmt"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

type ClusterView struct {
	Name string

	ServiceAccountPresent bool

	Cold bool
}

type Effect struct {
	Cluster  string
	State    v1alpha1.IdentityState
	DeleteSA bool
}

func Plan(clusters []ClusterView) []Effect {
	out := make([]Effect, 0, len(clusters))
	for _, c := range clusters {
		switch {
		case c.Cold:
			out = append(out, Effect{Cluster: c.Name, State: v1alpha1.IdentityColdDeferred})
		case c.ServiceAccountPresent:
			out = append(out, Effect{Cluster: c.Name, State: v1alpha1.IdentityRevocationPending, DeleteSA: true})
		default:
			out = append(out, Effect{Cluster: c.Name, State: v1alpha1.IdentityRevoked})
		}
	}
	return out
}

type ReplyEntry struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

func Reply(effects []Effect) []ReplyEntry {
	account := string(v1alpha1.IdentityRevoked)
	out := make([]ReplyEntry, 0, len(effects)+1)
	for _, e := range effects {
		if e.State != v1alpha1.IdentityRevoked {
			account = string(v1alpha1.IdentityRevocationPending)
		}
		out = append(out, ReplyEntry{Name: e.Cluster, State: string(e.State)})
	}
	return append([]ReplyEntry{{Name: "", State: account}}, out...)
}

type Settled interface{ settled() }

type settled struct{}

func (settled) settled() {}

func WakeSettled(e Effect) (Settled, error) {
	if e.State != v1alpha1.IdentityRevoked {
		return nil, fmt.Errorf(
			"revocation: refusing Ready for cluster %s: its revocation is %s and its ServiceAccount may still exist. "+
				"A COLD cluster is only safe to defer BECAUSE this gate runs before phase=Ready",
			e.Cluster, e.State)
	}
	return settled{}, nil
}

func Ready(s Settled) (v1alpha1.ClusterPhase, error) {
	if s == nil {
		return v1alpha1.ClusterInitializing, errors.New(
			"revocation: refusing Ready: the revocation reconcile did not run for this wake. " +
				"Set status.URL, run the reconcile, then call Ready with the Settled it returns")
	}
	return v1alpha1.ClusterReady, nil
}
