package gateway

import (
	"context"
	"errors"
)

const (
	EventCluster   = "cluster"
	EventMember    = "member"
	EventNodeClaim = "nodeClaim"
	EventInfo      = "info"

	InfoSnapshotComplete = "SnapshotComplete"
)

type ClusterEvent struct {
	Name            string `cbor:"name"`
	Phase           string `cbor:"phase"`
	Running         bool   `cbor:"running,omitempty"`
	Instance        string `cbor:"instance,omitempty"`
	Server          string `cbor:"server,omitempty"`
	Reason          string `cbor:"reason,omitempty"`
	Message         string `cbor:"message,omitempty"`
	TeardownBlocked bool   `cbor:"teardownBlocked,omitempty"`
	Deleted         bool   `cbor:"deleted,omitempty"`
	Snapshot        bool   `cbor:"snapshot,omitempty"`
	Time            string `cbor:"time"`
}

type MemberEvent struct {
	Cluster  string `cbor:"cluster"`
	Member   string `cbor:"member"`
	Phase    string `cbor:"phase"`
	Reason   string `cbor:"reason,omitempty"`
	Message  string `cbor:"message,omitempty"`
	Deleted  bool   `cbor:"deleted,omitempty"`
	Snapshot bool   `cbor:"snapshot,omitempty"`
	Time     string `cbor:"time"`
}

type NodeClaimEvent struct {
	Cluster       string `cbor:"cluster"`
	Name          string `cbor:"name"`
	NodeName      string `cbor:"nodeName,omitempty"`
	Phase         string `cbor:"phase"`
	Message       string `cbor:"message,omitempty"`
	ContractPhase string `cbor:"contractPhase,omitempty"`
	Outcome       string `cbor:"outcome,omitempty"`
	Bidder        string `cbor:"bidder,omitempty"`
	FQDN          string `cbor:"fqdn,omitempty"`
	Deleted       bool   `cbor:"deleted,omitempty"`
	Snapshot      bool   `cbor:"snapshot,omitempty"`
	Time          string `cbor:"time"`
}

type InfoEvent struct {
	Name    string `cbor:"name"`
	Message string `cbor:"message,omitempty"`
}

type Event struct {
	Type    string
	Payload any
}

type Emit func(Event) error

type Feed interface {
	Follow(ctx context.Context, caller Caller, name string, emit Emit) error
}

var (
	ErrTokenExpired = &XRPCError{"TokenExpired", "the token is expired; get a fresh one and reconnect", 401}

	ErrConsumerTooSlow = &XRPCError{"ConsumerTooSlow", "the client did not read frames fast enough", 429}

	ErrNoFeed = &XRPCError{
		"ClusterUnavailable",
		"this gateway was built without a cluster feed, so there is nothing to subscribe to",
		503,
	}
)

func (s *Server) SubscribeClusters(ctx context.Context, caller Caller, name string, emit Emit) error {
	if caller == "" {
		return ErrUnauthorized
	}
	if s.Feed == nil {
		return ErrNoFeed
	}
	err := s.Feed.Follow(ctx, caller, name, emit)
	if err == nil || errors.Is(err, context.Canceled) {
		return err
	}
	var xe *XRPCError
	if errors.As(err, &xe) {
		return xe
	}
	return &XRPCError{"ClusterUnavailable", "the cluster feed stopped: " + oneLine(err.Error()), 503}
}
