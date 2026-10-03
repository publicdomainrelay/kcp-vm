package sweepkcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpclient"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/plcsweep"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
)

const (
	testHost = "https://kcp.test:6443"
	didA     = "did:plc:aaaaaaaaaaaaaaaaaaaaaaaaaa"
	didB     = "did:plc:bbbbbbbbbbbbbbbbbbbbbbbbbb"
	ghost    = "did:plc:cccccccccccccccccccccccccc"
)

type request struct {
	Method string
	Path   string
}

type fakeKCP struct {
	t        *testing.T
	mu       sync.Mutex
	requests []request
	respond  func(request) (int, any)
}

func (f *fakeKCP) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, request{Method: r.Method, Path: r.URL.Path})
	f.mu.Unlock()

	status, obj := f.respond(request{Method: r.Method, Path: r.URL.Path})
	if status == 0 {
		f.t.Fatalf("unhandled request %s %s", r.Method, r.URL.Path)
	}
	var body []byte
	var err error
	if obj != nil {
		body, err = json.Marshal(obj)
		if err != nil {
			f.t.Fatalf("marshal response: %v", err)
		}
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    r,
	}, nil
}

func (f *fakeKCP) seen() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]request(nil), f.requests...)
}

func statusBody(reason string, code int) map[string]any {
	return map[string]any{
		"kind": "Status", "apiVersion": "v1", "status": "Failure",
		"reason": reason, "code": code, "message": reason,
	}
}

func notFound() (int, any) { return http.StatusNotFound, statusBody("NotFound", http.StatusNotFound) }

func workspaceList(names ...string) map[string]any {
	items := []any{}
	for _, n := range names {
		items = append(items, map[string]any{
			"kind": "Workspace", "apiVersion": "tenancy.kcp.io/v1alpha1",
			"metadata": map[string]any{"name": n}, "spec": map[string]any{},
		})
	}
	return map[string]any{
		"kind": "WorkspaceList", "apiVersion": "tenancy.kcp.io/v1alpha1",
		"metadata": map[string]any{}, "items": items,
	}
}

func mountedWorkspace(name string) map[string]any {
	return map[string]any{
		"kind": "Workspace", "apiVersion": "tenancy.kcp.io/v1alpha1",
		"metadata": map[string]any{"name": name},
		"spec": map[string]any{"mount": map[string]any{"ref": map[string]any{
			"apiVersion": "socialweb.computer/v1alpha1", "kind": "Cluster", "name": name,
		}}},
	}
}

func identity(did string, scope string) map[string]any {
	return map[string]any{
		"kind": "AtprotoIdentity", "apiVersion": "socialweb.computer/v1alpha1",
		"metadata": map[string]any{"name": "x"},
		"spec":     map[string]any{"did": did, "scope": scope},
	}
}

func identityList(records ...map[string]any) map[string]any {
	items := []any{}
	for _, r := range records {
		items = append(items, r)
	}
	return map[string]any{
		"kind": "AtprotoIdentityList", "apiVersion": "socialweb.computer/v1alpha1",
		"metadata": map[string]any{}, "items": items,
	}
}

func newWorklist(t *testing.T, f *fakeKCP) *Worklist {
	t.Helper()
	w, err := NewWorklist(Options{Host: testHost, Transport: f})
	if err != nil {
		t.Fatalf("NewWorklist: %v", err)
	}
	return w
}

const (
	workspacesPath = "/clusters/root/apis/tenancy.kcp.io/v1alpha1/workspaces"
	identitiesPath = "/apis/socialweb.computer/v1alpha1/atprotoidentities"
)

func accountIdentitiesPath(did string) string {
	return "/clusters/" + kcpclient.AccountPath(did) + identitiesPath
}

func TestTheWorklistIsTheAccountLevelRecords(t *testing.T) {
	hashA, hashB := kcpclient.AccountHash(didA), kcpclient.AccountHash(didB)
	f := &fakeKCP{t: t, respond: func(req request) (int, any) {
		switch req.Path {
		case workspacesPath:
			list := workspaceList(hashA, "system", hashB)
			list["items"] = append(list["items"].([]any), mountedWorkspace("clusters"))
			return http.StatusOK, list
		case accountIdentitiesPath(didA):
			return http.StatusOK, identityList(
				identity(didA, "Account"),
				identity(didA, "Cluster"),
				identity("", "Account"),
			)
		case accountIdentitiesPath(didB):
			return http.StatusOK, identityList(identity(didB, "Account"))
		case "/clusters/root:system" + identitiesPath:
			return http.StatusOK, identityList(identity(didA, "Cluster"))
		case "/clusters/root:clusters" + identitiesPath:
			return http.StatusOK, identityList(identity(ghost, "Account"))
		}
		return 0, nil
	}}
	w := newWorklist(t, f)

	dids, err := w.DIDs(context.Background())
	if err != nil {
		t.Fatalf("DIDs: %v", err)
	}
	if len(dids) != 2 || dids[0] != didA || dids[1] != didB {
		t.Fatalf("DIDs = %v, want [%s %s] (sorted, deduped, account-scoped only)", dids, didA, didB)
	}

	want := []request{
		{http.MethodGet, workspacesPath},
		{http.MethodGet, accountIdentitiesPath(didA)},
		{http.MethodGet, "/clusters/root:system" + identitiesPath},
		{http.MethodGet, accountIdentitiesPath(didB)},
	}
	got := f.seen()
	if len(got) != len(want) {
		t.Fatalf("made %d requests, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("request %d = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestASweepExemptIdentityIsNotSwept(t *testing.T) {
	const did = "did:plc:exempt000000000000000000000"
	exempt := identity(did, "Account")
	exempt["metadata"].(map[string]any)["annotations"] = map[string]any{
		SweepExemptAnnotation: SweepExemptValue,
	}
	hash := kcpclient.AccountHash(did)
	f := &fakeKCP{t: t, respond: func(req request) (int, any) {
		switch req.Path {
		case workspacesPath:
			return http.StatusOK, workspaceList(hash)
		case accountIdentitiesPath(did):
			return http.StatusOK, identityList(exempt)
		}
		return 0, nil
	}}
	w := newWorklist(t, f)

	dids, err := w.DIDs(context.Background())
	if err != nil {
		t.Fatalf("DIDs: %v", err)
	}
	if len(dids) != 0 {
		t.Fatalf("DIDs = %v, want none: the identity is marked exempt because this deployment "+
			"provisioned it itself and has no external PLC authority to check it against, so "+
			"sweeping it would revoke the account the deployment just created", dids)
	}
}

func TestAPartialWorklistIsNotAWorklist(t *testing.T) {
	hashA, hashB := kcpclient.AccountHash(didA), kcpclient.AccountHash(didB)
	f := &fakeKCP{t: t, respond: func(req request) (int, any) {
		switch req.Path {
		case workspacesPath:
			return http.StatusOK, workspaceList(hashA, hashB)
		case accountIdentitiesPath(didB):
			return http.StatusInternalServerError, statusBody("InternalError", http.StatusInternalServerError)
		}
		return http.StatusOK, identityList(identity(didA, "Account"))
	}}
	w := newWorklist(t, f)

	dids, err := w.DIDs(context.Background())
	if err == nil {
		t.Fatalf("an unreadable account workspace produced a WORKLIST of %v: a partial list of "+
			"the accounts this service granted reads as 'the rest no longer exist', and the "+
			"missing half stops being swept with nothing to show for it", dids)
	}
	if dids != nil {
		t.Fatalf("a failed enumeration returned DIDs as well: %v", dids)
	}
}

func TestAWorkspaceDeletedMidPassIsSkipped(t *testing.T) {
	hashA, hashB := kcpclient.AccountHash(didA), kcpclient.AccountHash(didB)
	f := &fakeKCP{t: t, respond: func(req request) (int, any) {
		switch req.Path {
		case workspacesPath:
			return http.StatusOK, workspaceList(hashA, hashB)
		case accountIdentitiesPath(didA):
			return notFound()
		}
		return http.StatusOK, identityList(identity(didB, "Account"))
	}}
	w := newWorklist(t, f)

	dids, err := w.DIDs(context.Background())
	if err != nil {
		t.Fatalf("a vanished workspace failed the whole pass: %v", err)
	}
	if len(dids) != 1 || dids[0] != didB {
		t.Fatalf("DIDs = %v, want just %s: the remaining accounts must still be checked", dids, didB)
	}
}

func TestAListFailureFailsThePass(t *testing.T) {
	f := &fakeKCP{t: t, respond: func(request) (int, any) {
		return http.StatusForbidden, statusBody("Forbidden", http.StatusForbidden)
	}}
	w := newWorklist(t, f)

	dids, err := w.DIDs(context.Background())
	if err == nil {
		t.Fatalf("an unreadable root produced a worklist of %v: an empty worklist means the "+
			"sweep runs forever and checks nothing", dids)
	}
}

func TestTheWorklistSatisfiesTheSeam(t *testing.T) {
	f := &fakeKCP{t: t, respond: func(request) (int, any) { return http.StatusOK, workspaceList() }}
	var seam plcsweep.Worklist = newWorklist(t, f)
	if _, err := seam.DIDs(context.Background()); err != nil {
		t.Fatalf("DIDs: %v", err)
	}
	if _, err := NewWorklist(Options{}); err == nil {
		t.Fatal("an empty Host was accepted, so the client has nothing to dial")
	}
}

type trace struct {
	mu     sync.Mutex
	events []string
}

func (t *trace) add(e string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.events = append(t.events, e)
}

func (t *trace) all() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.events...)
}

type denyWriter struct {
	tr  *trace
	err error
}

func (d denyWriter) WriteDeny(_ context.Context, did string) error {
	d.tr.add("deny:" + did)
	return d.err
}

type fanOut struct {
	tr    *trace
	views []revocation.ClusterView
	err   error
}

func (f fanOut) FanOut(_ context.Context, did string) ([]revocation.ClusterView, error) {
	f.tr.add("fanout:" + did)
	return f.views, f.err
}

type eraser struct {
	tr   *trace
	fail map[string]bool
}

func (e eraser) DeleteServiceAccount(_ context.Context, _, cluster string) error {
	e.tr.add("erase:" + cluster)
	if e.fail[cluster] {
		return errors.New("kcpclient: delete the ServiceAccount: cluster is not answering")
	}
	return nil
}

func TestTheDenyIsWrittenFirst(t *testing.T) {
	tr := &trace{}
	var plans []Plan
	r := &Revoker{
		Deny:   denyWriter{tr: tr},
		FanOut: fanOut{tr: tr, views: []revocation.ClusterView{{Name: "web", ServiceAccountPresent: true}}},
		Erase:  eraser{tr: tr},
		OnPlan: func(p Plan) { plans = append(plans, p) },
	}

	if err := r.Revoke(context.Background(), didA, "plc answered 404"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	want := []string{"deny:" + didA, "fanout:" + didA, "erase:web"}
	got := tr.all()
	if len(got) != len(want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v: a revocation that observes before it writes leaves a "+
				"window in which the caller holds a valid identity and no deny, and one that "+
				"deletes before it observes deletes from a plan nobody computed", got, want)
		}
	}
	if len(plans) != 1 || plans[0].DID != didA || plans[0].Why != "plc answered 404" {
		t.Fatalf("the plan did not carry the reason the sweep gave: %+v", plans)
	}
}

func TestAnUnwrittenDenyIsNotFollowedByAFanOut(t *testing.T) {
	tr := &trace{}
	r := &Revoker{
		Deny:   denyWriter{tr: tr, err: errors.New("kcp refused the write")},
		FanOut: fanOut{tr: tr},
		OnPlan: func(Plan) { t.Error("a plan was reported for a deny that was never written") },
	}

	err := r.Revoke(context.Background(), didA, "plc answered 404")
	if err == nil {
		t.Fatal("a failed deny write was reported as a successful revocation")
	}
	if !strings.Contains(err.Error(), "kcp refused the write") {
		t.Fatalf("the error does not carry kcp's refusal: %v", err)
	}
	if got := tr.all(); len(got) != 1 {
		t.Fatalf("trace = %v, want only the write: nothing may happen after a failed deny", got)
	}
}

func TestANilDenyWriterIsRefused(t *testing.T) {
	r := &Revoker{}
	if err := r.Revoke(context.Background(), didA, "why"); err == nil {
		t.Fatal("a Revoker with no writer reported a revocation")
	}
}

func TestAFanOutFailureIsNotAFailedDeny(t *testing.T) {
	tr := &trace{}
	var plans []Plan
	r := &Revoker{
		Deny:   denyWriter{tr: tr},
		FanOut: fanOut{tr: tr, err: errors.New("cluster apiserver is unreachable")},
		OnPlan: func(p Plan) { plans = append(plans, p) },
	}

	if err := r.Revoke(context.Background(), didA, "plc answered 404"); err != nil {
		t.Fatalf("Revoke returned %v for a fan-out failure: plcsweep would report "+
			"'DENY NOT WRITTEN' about a deny that WAS written", err)
	}
	if len(plans) != 1 {
		t.Fatalf("want one plan, got %+v", plans)
	}
	if plans[0].Err == nil {
		t.Fatal("the unobservable fan-out was reported as a successful plan")
	}
	if len(plans[0].Entries) != 0 {
		t.Fatalf("entries were invented for an observation that never happened: %+v", plans[0].Entries)
	}
	if got := tr.all(); got[0] != "deny:"+didA {
		t.Fatalf("trace = %v, want the deny written anyway", got)
	}
}

func TestANilFanOutStillWritesTheDeny(t *testing.T) {
	tr := &trace{}
	var plans []Plan
	r := &Revoker{Deny: denyWriter{tr: tr}, OnPlan: func(p Plan) { plans = append(plans, p) }}

	if err := r.Revoke(context.Background(), didA, "plc document carries no verification key"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if got := tr.all(); len(got) != 1 || got[0] != "deny:"+didA {
		t.Fatalf("trace = %v, want the deny written", got)
	}
	if len(plans) != 1 || plans[0].Err == nil || !strings.Contains(plans[0].Err.Error(), "no FanOut") {
		t.Fatalf("an unwired fan-out was not reported as a gap: %+v", plans)
	}
}

func TestThePlanIsTheExistingDecision(t *testing.T) {
	var plans []Plan
	tr := &trace{}
	r := &Revoker{
		Deny: denyWriter{tr: &trace{}},
		FanOut: fanOut{tr: &trace{}, views: []revocation.ClusterView{
			{Name: "cold", Cold: true},
			{Name: "up", ServiceAccountPresent: true},
			{Name: "done"},
		}},
		Erase:  eraser{tr: tr},
		OnPlan: func(p Plan) { plans = append(plans, p) },
	}
	if err := r.Revoke(context.Background(), didA, "plc answered 404"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	want := []revocation.ReplyEntry{
		{Name: "", State: "RevocationPending"},
		{Name: "cold", State: "ColdDeferred"},
		{Name: "up", State: "Revoked"},
		{Name: "done", State: "Revoked"},
	}
	if len(plans) != 1 || len(plans[0].Entries) != len(want) {
		t.Fatalf("plan = %+v, want %+v", plans, want)
	}
	for i, w := range want {
		if plans[0].Entries[i] != w {
			t.Errorf("entry %d = %+v, want %+v", i, plans[0].Entries[i], w)
		}
	}
	if got := tr.all(); len(got) != 1 || got[0] != "erase:up" {
		t.Fatalf("deletes = %v, want only the cluster that still holds a ServiceAccount", got)
	}
	if plans[0].Err != nil {
		t.Fatalf("a plan with an eraser wired reported a gap: %v", plans[0].Err)
	}
}

func TestAFailedDeleteIsReportedAsPendingAndNeverAsDone(t *testing.T) {
	tr := &trace{}
	var plans []Plan
	r := &Revoker{
		Deny:   denyWriter{tr: &trace{}},
		FanOut: fanOut{tr: &trace{}, views: []revocation.ClusterView{{Name: "web", ServiceAccountPresent: true}}},
		Erase:  eraser{tr: tr, fail: map[string]bool{"web": true}},
		OnPlan: func(p Plan) { plans = append(plans, p) },
	}

	if err := r.Revoke(context.Background(), didA, "plc answered 404"); err != nil {
		t.Fatalf("Revoke returned %v: the deny WAS written, and plcsweep would print this as "+
			"(DENY NOT WRITTEN: ...)", err)
	}
	want := []revocation.ReplyEntry{
		{Name: "", State: "RevocationPending"},
		{Name: "web", State: "RevocationPending"},
	}
	if len(plans) != 1 || len(plans[0].Entries) != 2 {
		t.Fatalf("plan = %+v, want %+v", plans, want)
	}
	for i, w := range want {
		if plans[0].Entries[i] != w {
			t.Errorf("entry %d = %+v, want %+v", i, plans[0].Entries[i], w)
		}
	}
	if got := tr.all(); len(got) != 1 || got[0] != "erase:web" {
		t.Fatalf("the delete was %v, want one attempt on web", got)
	}
}

func TestANilEraserIsRefusedAndNotSkipped(t *testing.T) {
	var plans []Plan
	r := &Revoker{
		Deny: denyWriter{tr: &trace{}},
		FanOut: fanOut{tr: &trace{}, views: []revocation.ClusterView{
			{Name: "cold", Cold: true},
			{Name: "web", ServiceAccountPresent: true},
		}},
		OnPlan: func(p Plan) { plans = append(plans, p) },
	}

	if err := r.Revoke(context.Background(), didA, "plc answered 404"); err != nil {
		t.Fatalf("Revoke returned %v for a missing effect half: plcsweep would report "+
			"'DENY NOT WRITTEN' about a deny that WAS written", err)
	}
	if len(plans) != 1 || plans[0].Err == nil || !strings.Contains(plans[0].Err.Error(), "no Eraser") {
		t.Fatalf("an unwired effect half was not reported as a gap: %+v", plans)
	}
	want := []revocation.ReplyEntry{
		{Name: "", State: "RevocationPending"},
		{Name: "cold", State: "ColdDeferred"},
		{Name: "web", State: "RevocationPending"},
	}
	if len(plans[0].Entries) != len(want) {
		t.Fatalf("entries = %+v, want %+v", plans[0].Entries, want)
	}
	for i, w := range want {
		if plans[0].Entries[i] != w {
			t.Errorf("entry %d = %+v, want %+v", i, plans[0].Entries[i], w)
		}
	}
}

func TestANilEraserWithNothingToDeleteIsNotAnAlarm(t *testing.T) {
	var plans []Plan
	r := &Revoker{
		Deny: denyWriter{tr: &trace{}},
		FanOut: fanOut{tr: &trace{}, views: []revocation.ClusterView{
			{Name: "cold", Cold: true},
			{Name: "done"},
		}},
		OnPlan: func(p Plan) { plans = append(plans, p) },
	}

	if err := r.Revoke(context.Background(), didA, "plc answered 404"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if len(plans) != 1 || plans[0].Err != nil {
		t.Fatalf("a pass that owed no delete reported a gap: %+v", plans)
	}
}

func TestTheFanOutSeamComposesWithTheRealClient(t *testing.T) {
	var _ DenyWriter = (*kcpclient.Client)(nil)
	var _ FanOut = (*kcpclient.Client)(nil)
	var _ Eraser = (*kcpclient.Client)(nil)
	c, err := kcpclient.New(kcpclient.Options{Host: testHost})
	if err != nil {
		t.Fatalf("kcpclient.New: %v", err)
	}
	if c == nil {
		t.Fatal("kcpclient.New returned no client for the seam to compose with")
	}
}

func workspace(name string) map[string]any {
	return map[string]any{
		"kind": "Workspace", "apiVersion": "tenancy.kcp.io/v1alpha1",
		"metadata": map[string]any{"name": name}, "spec": map[string]any{},
	}
}

func serviceAccount(did string) map[string]any {
	return map[string]any{
		"kind": "ServiceAccount", "apiVersion": "v1",
		"metadata": map[string]any{"name": kcpclient.ServiceAccountName(did), "namespace": "users"},
	}
}

func TestTheWholeActAgainstTheRealClient(t *testing.T) {
	webPath := "/clusters/" + kcpclient.MountPath(didA, "web") + "/api/v1/namespaces/users/serviceaccounts/" + kcpclient.ServiceAccountName(didA)
	dbPath := "/clusters/" + kcpclient.MountPath(didA, "db") + "/api/v1/namespaces/users/serviceaccounts/" + kcpclient.ServiceAccountName(didA)

	f := &fakeKCP{t: t, respond: func(req request) (int, any) {
		switch {
		case req.Method == http.MethodDelete:
			return http.StatusOK, statusBody("Success", http.StatusOK)
		case req.Path == webPath:
			return http.StatusOK, serviceAccount(didA)
		case req.Path == dbPath:
			return http.StatusServiceUnavailable, statusBody("ServiceUnavailable", http.StatusServiceUnavailable)
		case strings.HasSuffix(req.Path, "/apis/tenancy.kcp.io/v1alpha1/workspaces"):
			return http.StatusOK, workspaceList("web", "db")
		case strings.Contains(req.Path, "/apis/tenancy.kcp.io/v1alpha1/workspaces/"):
			return http.StatusOK, workspace(kcpclient.AccountHash(didA))
		case strings.Contains(req.Path, "/atprotoidentities/"):
			return http.StatusOK, identity(didA, "Account")
		}
		return 0, nil
	}}
	c, err := kcpclient.New(kcpclient.Options{Host: testHost, Transport: f})
	if err != nil {
		t.Fatalf("kcpclient.New: %v", err)
	}

	var plans []Plan
	r := &Revoker{Deny: c, FanOut: c, Erase: c, OnPlan: func(p Plan) { plans = append(plans, p) }}
	if err := r.Revoke(context.Background(), didA, "plc answered 404"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	want := []revocation.ReplyEntry{
		{Name: "", State: "RevocationPending"},
		{Name: "db", State: "ColdDeferred"},
		{Name: "web", State: "Revoked"},
	}
	if len(plans) != 1 || len(plans[0].Entries) != len(want) {
		t.Fatalf("plan = %+v, want %+v", plans, want)
	}
	for i, w := range want {
		if plans[0].Entries[i] != w {
			t.Errorf("entry %d = %+v, want %+v", i, plans[0].Entries[i], w)
		}
	}

	var patched, deleted = -1, -1
	for i, got := range f.seen() {
		switch {
		case got.Method == http.MethodPatch:
			patched = i
		case got.Method == http.MethodDelete:
			if deleted >= 0 {
				t.Fatalf("two DELETEs: only the cluster that still holds a ServiceAccount owes one")
			}
			deleted = i
			if got.Path != webPath {
				t.Errorf("deleted %s, want %s -- the COLD cluster is not deleted, and the warm "+
					"one is deleted by its derived name in ns=users", got.Path, webPath)
			}
		}
	}
	if patched < 0 {
		t.Fatal("no PATCH: the deny was never written")
	}
	if deleted < 0 {
		t.Fatal("no DELETE: the plan's effect was computed and discarded again")
	}
	if patched > deleted {
		t.Fatalf("the PATCH is at request %d and the DELETE at %d: the deny must be written "+
			"BEFORE any effect is applied", patched, deleted)
	}
}

func TestTheRevokerSatisfiesTheSeam(t *testing.T) {
	var seam plcsweep.Revoker = &Revoker{Deny: denyWriter{tr: &trace{}}}
	if err := seam.Revoke(context.Background(), didA, "why"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
}
