package plcsweep

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
)

type stubWorklist struct {
	dids []string
	err  error
}

func (w stubWorklist) DIDs(context.Context) ([]string, error) { return w.dids, w.err }

type stubResolver struct {
	byDID map[string]revocation.PLCResolution
	errs  map[string]error
}

func (r stubResolver) Resolve(_ context.Context, did string) (revocation.PLCResolution, error) {
	if err := r.errs[did]; err != nil {
		return revocation.PLCResolution{}, err
	}
	return r.byDID[did], nil
}

type stubRevoker struct {
	revoked []string
	whys    []string
	err     error
}

func (v *stubRevoker) Revoke(_ context.Context, did, why string) error {
	if v.err != nil {
		return v.err
	}
	v.revoked = append(v.revoked, did)
	v.whys = append(v.whys, why)
	return nil
}

func sweeper(w stubWorklist, r stubResolver, v *stubRevoker) *Sweeper {
	return &Sweeper{Worklist: w, Resolver: r, Revoker: v}
}

func TestAPlcOutageRevokesNobody(t *testing.T) {
	outage := errors.New("dial tcp: connection refused")
	v := &stubRevoker{}
	s := sweeper(
		stubWorklist{dids: []string{"did:plc:a", "did:plc:b"}},
		stubResolver{errs: map[string]error{"did:plc:a": outage, "did:plc:b": outage}},
		v,
	)

	out, err := s.Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if len(v.revoked) != 0 {
		t.Fatalf("A PLC OUTAGE REVOKED %v -- a fetch failure read as an absence revokes "+
			"every user of the service on the first outage", v.revoked)
	}
	for _, o := range out {
		if !o.Skipped {
			t.Fatalf("%s was not marked Skipped, so an operator cannot tell 'we looked and "+
				"it is fine' from 'we could not look': %+v", o.DID, o)
		}
	}
}

func TestAnOutageDoesNotStopThePass(t *testing.T) {
	v := &stubRevoker{}
	s := sweeper(
		stubWorklist{dids: []string{"did:plc:unreachable", "did:plc:deleted"}},
		stubResolver{
			errs:  map[string]error{"did:plc:unreachable": errors.New("timeout")},
			byDID: map[string]revocation.PLCResolution{"did:plc:deleted": {Found: false}},
		},
		v,
	)
	if _, err := s.Pass(context.Background()); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if len(v.revoked) != 1 || v.revoked[0] != "did:plc:deleted" {
		t.Fatalf("a 404 behind an outage was not revoked: %v -- an error must stop ONE "+
			"did, not the pass", v.revoked)
	}
}

func TestA404Revokes(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  revocation.PLCResolution
		want bool
	}{
		{"the account is gone", revocation.PLCResolution{Found: false}, true},
		{"a document with no key authenticates as nothing", revocation.PLCResolution{Found: true}, true},
		{"a live document", revocation.PLCResolution{Found: true, Keys: []string{"did:key:z1"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &stubRevoker{}
			s := sweeper(
				stubWorklist{dids: []string{"did:plc:a"}},
				stubResolver{byDID: map[string]revocation.PLCResolution{"did:plc:a": tc.res}},
				v,
			)
			if _, err := s.Pass(context.Background()); err != nil {
				t.Fatalf("Pass: %v", err)
			}
			if got := len(v.revoked) == 1; got != tc.want {
				t.Fatalf("revoked=%v, want %v (why: %v)", got, tc.want, v.whys)
			}
		})
	}
}

func TestAKeyRotationIsNotARevocation(t *testing.T) {
	v := &stubRevoker{}
	s := sweeper(
		stubWorklist{dids: []string{"did:plc:a"}},
		stubResolver{byDID: map[string]revocation.PLCResolution{
			"did:plc:a": {Found: true, Keys: []string{"did:key:zDIFFERENT"}},
		}},
		v,
	)
	if _, err := s.Pass(context.Background()); err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if len(v.revoked) != 0 {
		t.Fatal("a key ROTATION revoked the identity; rotating a signing key is normal " +
			"and treating it as a deletion revokes live users")
	}
}

func TestAFailedDenyWriteIsLoudRatherThanSilent(t *testing.T) {
	v := &stubRevoker{err: errors.New("kcp refused the write")}
	s := sweeper(
		stubWorklist{dids: []string{"did:plc:a"}},
		stubResolver{byDID: map[string]revocation.PLCResolution{"did:plc:a": {Found: false}}},
		v,
	)
	out, err := s.Pass(context.Background())
	if err != nil {
		t.Fatalf("Pass: %v", err)
	}
	if len(out) != 1 || !strings.Contains(out[0].Why, "DENY NOT WRITTEN") {
		t.Fatalf("a failed deny write was reported as an ordinary revocation: %+v", out)
	}
}

func TestAWorklistFailureStopsThePass(t *testing.T) {
	v := &stubRevoker{}
	s := sweeper(stubWorklist{err: errors.New("kcp unreachable")}, stubResolver{}, v)
	if _, err := s.Pass(context.Background()); err == nil {
		t.Fatal("an unreadable worklist was reported as a successful pass")
	}
}

func TestRunRefusesANonPositiveInterval(t *testing.T) {
	s := sweeper(stubWorklist{}, stubResolver{}, &stubRevoker{})
	if err := s.Run(context.Background(), 0, nil); !errors.Is(err, ErrNoInterval) {
		t.Fatalf("want ErrNoInterval, got %v -- a zero interval is a hot loop against a "+
			"third party", err)
	}
}

func TestRunPassesImmediately(t *testing.T) {
	v := &stubRevoker{}
	s := sweeper(
		stubWorklist{dids: []string{"did:plc:a"}},
		stubResolver{byDID: map[string]revocation.PLCResolution{"did:plc:a": {Found: false}}},
		v,
	)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	passes := 0
	_ = s.Run(ctx, time.Hour, func([]Outcome) { passes++ })
	if passes == 0 {
		t.Fatal("the sweep waited a full interval before its first pass")
	}
	if len(v.revoked) != 1 {
		t.Fatalf("the immediate pass did not revoke: %v", v.revoked)
	}
}
