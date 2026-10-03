package mint

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type fakeStore struct {
	binding    Binding
	resolveErr error
	issues     int
	token      string
	expires    time.Time
	issueErr   error
}

func (f *fakeStore) Resolve(context.Context, DeviceCredential) (Binding, error) {
	if f.resolveErr != nil {
		return Binding{}, f.resolveErr
	}
	return f.binding, nil
}

func (f *fakeStore) Issue(_ context.Context, _ Binding, ttl time.Duration) (string, time.Time, error) {
	f.issues++
	if f.issueErr != nil {
		return "", time.Time{}, f.issueErr
	}
	return f.token, f.expires, nil
}

func ok() (*fakeStore, *Mint, time.Time) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	s := &fakeStore{
		binding: Binding{DID: "did:plc:a", Cluster: "web", SA: "did-web-example-com", SAExists: true, Instance: "1"},
		token:   "sa-token",
		expires: now.Add(15 * time.Minute),
	}
	return s, New(s, Options{}), now
}

func TestTheCredentialAlwaysCarriesAnExpirationTimestamp(t *testing.T) {
	_, m, now := ok()
	cred, err := m.Issue(context.Background(), DeviceCredential{Token: "d"}, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if cred.Status.ExpirationTimestamp == "" {
		t.Fatal("no expirationTimestamp: client-go will reuse the FIRST token for the " +
			"life of the process and a 5-minute token will keep being presented long " +
			"after it expires, with no error and no warning")
	}
	ts, err := time.Parse(time.RFC3339, cred.Status.ExpirationTimestamp)
	if err != nil {
		t.Fatalf("expirationTimestamp is not RFC3339: %q", cred.Status.ExpirationTimestamp)
	}
	if !ts.After(now) {
		t.Fatalf("expirationTimestamp %s is not in the future relative to %s", ts, now)
	}
	if !ts.Before(now.Add(15 * time.Minute)) {
		t.Fatalf("the credential must expire BEFORE the token, so the client renews "+
			"while the old one still works; got %s against a token expiring %s",
			ts, now.Add(15*time.Minute))
	}
}

func TestTheCredentialIsNonInteractive(t *testing.T) {
	_, m, now := ok()
	cred, err := m.Issue(context.Background(), DeviceCredential{Token: "d"}, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if cred.Spec.Interactive {
		t.Fatal("the credential claims interactive: a runner is a non-TTY process, and " +
			"the plugin never runs at all")
	}
	if cred.APIVersion != APIVersion || cred.Kind != Kind {
		t.Fatalf("wrong envelope: %s/%s", cred.APIVersion, cred.Kind)
	}
}

func TestADeletedServiceAccountIsRefusedEvenWithAValidCredential(t *testing.T) {
	s, m, now := ok()
	s.binding.SAExists = false

	_, err := m.Issue(context.Background(), DeviceCredential{Token: "d"}, now)
	if !errors.Is(err, ErrServiceAccountGone) {
		t.Fatalf("want ErrServiceAccountGone, got %v", err)
	}
	if s.issues != 0 {
		t.Fatalf("a token was issued for a DELETED ServiceAccount: revocation does not " +
			"take effect on the renewal path, which is the only path a runner uses")
	}
}

func TestAFailureIsHeldSoTheStormIsBounded(t *testing.T) {
	s, m, now := ok()
	s.binding.SAExists = false

	if _, err := m.Issue(context.Background(), DeviceCredential{Token: "d"}, now); err == nil {
		t.Fatal("expected a refusal")
	}
	readsBefore := s.issues

	_, err := m.Issue(context.Background(), DeviceCredential{Token: "d"}, now.Add(10*time.Second))
	if !errors.Is(err, ErrHeld) {
		t.Fatalf("want ErrHeld during the hold, got %v -- without a hold, one bad "+
			"credential becomes one call per client request", err)
	}
	if s.issues != readsBefore {
		t.Fatal("the hold dialled anyway")
	}

	s.binding.SAExists = true
	_, err = m.Issue(context.Background(), DeviceCredential{Token: "d"}, now.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("the hold did not expire: %v", err)
	}
}

func TestTheHoldIsPerCredential(t *testing.T) {
	s, m, now := ok()
	s.binding.SAExists = false
	_, _ = m.Issue(context.Background(), DeviceCredential{Token: "bad"}, now)

	s.binding.SAExists = true
	if _, err := m.Issue(context.Background(), DeviceCredential{Token: "good"}, now.Add(time.Second)); err != nil {
		t.Fatalf("one credential's failure held another: %v", err)
	}
}

func TestAnEmptyTokenIsNeverReturned(t *testing.T) {
	s, m, now := ok()
	s.token = ""
	if _, err := m.Issue(context.Background(), DeviceCredential{Token: "d"}, now); err == nil {
		t.Fatal("an empty token was returned; the plugin would print it and client-go " +
			"would send it and repeat SILENTLY")
	}
}

func TestTheResponseIsTheVerbatimExecCredentialShape(t *testing.T) {
	_, m, now := ok()
	cred, err := m.Issue(context.Background(), DeviceCredential{Token: "d"}, now)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"apiVersion", "kind", "spec", "status"} {
		if _, ok := back[k]; !ok {
			t.Fatalf("the ExecCredential envelope is missing %q: %v", k, back)
		}
	}
	status, _ := back["status"].(map[string]any)
	for _, k := range []string{"token", "expirationTimestamp"} {
		if _, ok := status[k]; !ok {
			t.Fatalf("status is missing %q -- the plugin prints this body UNCHANGED, so "+
				"a field missing here is a field missing on the wire: %v", k, status)
		}
	}
}
