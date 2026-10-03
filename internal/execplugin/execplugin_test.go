package execplugin

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

type fakeMinter struct {
	cred  Credential
	err   error
	calls int
}

func (f *fakeMinter) Mint(context.Context, string) (Credential, error) {
	f.calls++
	if f.err != nil {
		return Credential{}, f.err
	}
	return f.cred, nil
}

func good(expiry time.Time) Credential {
	return Credential{
		APIVersion: "client.authentication.k8s.io/v1",
		Kind:       "ExecCredential",
		Status: CredentialState{
			Token:               "sa-token",
			ExpirationTimestamp: expiry.UTC().Format(time.RFC3339),
		},
	}
}

func opts(t *testing.T) Options {
	t.Helper()
	return Options{CachePath: filepath.Join(t.TempDir(), "cred.json"), Now: func() time.Time { return now }}
}

func TestAFreshMintIsPrintedVerbatim(t *testing.T) {
	m := &fakeMinter{cred: good(now.Add(15 * time.Minute))}
	out, code, err := Run(context.Background(), m, "device-key", opts(t))
	if err != nil || code != ExitOK {
		t.Fatalf("Run: code=%d err=%v", code, err)
	}
	var back Credential
	if err := json.Unmarshal([]byte(out), &back); err != nil {
		t.Fatalf("the printed body is not the ExecCredential: %v", err)
	}
	if back.Status.Token != "sa-token" || back.Status.ExpirationTimestamp == "" {
		t.Fatalf("printed %+v", back.Status)
	}
}

func TestACredentialWithNoExpirationIsRefused(t *testing.T) {
	c := good(now.Add(time.Hour))
	c.Status.ExpirationTimestamp = ""
	m := &fakeMinter{cred: c}

	out, code, err := Run(context.Background(), m, "device-key", opts(t))
	if err == nil {
		t.Fatal("a credential with no expirationTimestamp was accepted")
	}
	if code != ExitFailure || out != "" {
		t.Fatalf("want (no body, exit %d), got (%q, %d)", ExitFailure, out, code)
	}
}

func TestAnEmptyTokenIsNeverPrinted(t *testing.T) {
	c := good(now.Add(time.Hour))
	c.Status.Token = ""
	m := &fakeMinter{cred: c}

	out, code, err := Run(context.Background(), m, "device-key", opts(t))
	if err == nil || code != ExitFailure || out != "" {
		t.Fatalf("an empty token must refuse with exit %d and no body; got (%q, %d, %v)",
			ExitFailure, out, code, err)
	}
}

func TestAnAlreadyExpiredCredentialIsRefused(t *testing.T) {
	m := &fakeMinter{cred: good(now.Add(-time.Minute))}
	out, code, err := Run(context.Background(), m, "device-key", opts(t))
	if err == nil || code != ExitFailure || out != "" {
		t.Fatalf("an expired credential must refuse; got (%q, %d, %v)", out, code, err)
	}
}

func TestAMintFailureIsExit3(t *testing.T) {
	m := &fakeMinter{err: errors.New("503")}
	out, code, err := Run(context.Background(), m, "device-key", opts(t))
	if err == nil || code != ExitFailure || out != "" {
		t.Fatalf("a mint failure must be exit %d with nothing on stdout; got (%q, %d, %v)",
			ExitFailure, out, code, err)
	}
}

func TestTheCacheStopsTheStorm(t *testing.T) {
	o := opts(t)
	m := &fakeMinter{cred: good(now.Add(15 * time.Minute))}

	if _, code, err := Run(context.Background(), m, "device-key", o); err != nil || code != ExitOK {
		t.Fatalf("first Run: %d %v", code, err)
	}
	if m.calls != 1 {
		t.Fatalf("first call minted %d times, want 1", m.calls)
	}
	for i := 0; i < 5; i++ {
		if _, code, err := Run(context.Background(), m, "device-key", o); err != nil || code != ExitOK {
			t.Fatalf("cached Run %d: %d %v", i, code, err)
		}
	}
	if m.calls != 1 {
		t.Fatalf("the mint was called %d times; A CACHE IN PROCESS MEMORY IS WORTHLESS "+
			"BECAUSE EACH INVOCATION IS A NEW PROCESS -- it must be on disk", m.calls)
	}
}

func TestTheCacheIsKeyedOnTheDeviceKey(t *testing.T) {
	o := opts(t)
	m := &fakeMinter{cred: good(now.Add(15 * time.Minute))}

	if _, _, err := Run(context.Background(), m, "cluster-A-key", o); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if _, code, err := Run(context.Background(), m, "cluster-B-key", o); err != nil || code != ExitOK {
		t.Fatalf("second Run: %d %v", code, err)
	}
	if m.calls != 2 {
		t.Fatalf("the mint was called %d times for two different device keys; a cache "+
			"keyed on nothing would hand one cluster's token to another's helper", m.calls)
	}
}

func TestTheCacheIsNotReusedInsideTheSkew(t *testing.T) {
	o := opts(t)
	m := &fakeMinter{cred: good(now.Add(10 * time.Second))}

	if _, _, err := Run(context.Background(), m, "k", o); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if _, _, err := Run(context.Background(), m, "k", o); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if m.calls != 2 {
		t.Fatalf("mint called %d times; a credential within the skew of expiry must not "+
			"be reused, because it can expire between the check and the request", m.calls)
	}
}

func TestNoDeviceKeyIsExit3(t *testing.T) {
	m := &fakeMinter{cred: good(now.Add(time.Hour))}
	if out, code, err := Run(context.Background(), m, "", opts(t)); err == nil || code != ExitFailure || out != "" {
		t.Fatalf("a missing device key must be exit %d; got (%q, %d, %v)", ExitFailure, out, code, err)
	}
}
