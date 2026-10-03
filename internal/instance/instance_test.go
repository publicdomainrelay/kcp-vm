package instance

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type flipReader struct{ used bool }

func (f *flipReader) Read(p []byte) (int, error) {
	for i := range p {
		if f.used {
			p[i] = 0xff
		} else {
			p[i] = 0x00
		}
	}
	f.used = true
	return len(p), nil
}

func TestTokensAreDistinctAndUnguessable(t *testing.T) {
	seen := map[Token]bool{}
	for i := 0; i < 64; i++ {
		tok, err := New(nil)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if seen[tok] {
			t.Fatalf("New returned a duplicate token on iteration %d", i)
		}
		seen[tok] = true
		if len(tok) != 3+TokenBytes*2 {
			t.Fatalf("token length = %d, want %d", len(tok), 3+TokenBytes*2)
		}
	}
}

func TestNewIsDeterministicGivenTheReader(t *testing.T) {
	a, err := New(&flipReader{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	b, err := New(&flipReader{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a != b {
		t.Fatalf("the reader is ignored: %q vs %q", a, b)
	}
	if !strings.HasPrefix(string(a), "k10") {
		t.Fatalf("token %q has no recognisable prefix", a)
	}
}

func TestPlanKeepsTheDangerousStep(t *testing.T) {
	plan, err := PlanFor("k10aaaa", true)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if !plan.DeleteBootstrapRow {
		t.Fatalf("a datastore that HOLDS A ROW must have it deleted: the row is encrypted "+
			"with the previous instance's token, and k3s exits with 'bootstrap data "+
			"already found and encrypted with different token'. plan = %+v", plan)
	}
	if !plan.ReSeedBundle {
		t.Fatal("deleting the row without re-seeding leaves k3s with nothing to " +
			"author from, and pre-seeding is safe precisely BECAUSE there is no row to " +
			"reconcile against")
	}
	if plan.Reason == "" {
		t.Fatal("an operator reading a log needs to know why their cluster is being " +
			"rebuilt rather than restarted")
	}
}

func TestAFreshDatastoreDoesNotDeleteOrReSeed(t *testing.T) {
	plan, err := PlanFor("k10bbbb", false)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if plan.DeleteBootstrapRow || plan.ReSeedBundle {
		t.Fatalf("a fresh datastore has nothing to delete and nothing to disagree with; "+
			"pre-seeding there would substitute our bytes for the ones k3s is about to "+
			"author. plan = %+v", plan)
	}
}

func TestTheDangerousDirectionIsTheAssumption(t *testing.T) {
	assumedFresh, err := PlanFor("k10cccc", false)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	actuallyHasRow, err := PlanFor("k10cccc", true)
	if err != nil {
		t.Fatalf("PlanFor: %v", err)
	}
	if assumedFresh.DeleteBootstrapRow == actuallyHasRow.DeleteBootstrapRow {
		t.Fatal("the two datastore states produced the same plan, so the caller's " +
			"observation does not reach the decision")
	}
	if !actuallyHasRow.DeleteBootstrapRow {
		t.Fatal("the true state must delete; assuming otherwise is what produces a cluster " +
			"that will not come up")
	}
}

func TestAnEmptyTokenIsRefused(t *testing.T) {
	if _, err := PlanFor("", true); !errors.Is(err, ErrEmptyToken) {
		t.Fatalf("want ErrEmptyToken, got %v", err)
	}
}

func TestNewSurfacesAReaderFailure(t *testing.T) {
	if _, err := New(errReader{}); err == nil {
		t.Fatal("a failing entropy source produced a token -- it would be predictable and " +
			"the failure would be invisible")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("no entropy") }

func TestTheTokenIsNotDerivedFromAnythingGuessable(t *testing.T) {
	zeros, _ := New(bytes.NewReader(make([]byte, TokenBytes)))
	ffs, _ := New(bytes.NewReader(bytes.Repeat([]byte{0xff}, TokenBytes)))
	if zeros == ffs {
		t.Fatal("different entropy produced the same token")
	}
}
