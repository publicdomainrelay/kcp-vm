package kcpclient

import (
	"context"
	"net/http"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

func linkPath(name string) string {
	return "/clusters/" + homePath + "/apis/socialweb.computer/v1alpha1/clusterlinks/" + name
}

func listeningLink() v1alpha1.ClusterLink {
	return v1alpha1.ClusterLink{
		ObjectMeta: metav1.ObjectMeta{Name: "3hfr4o24-web-apiserver"},
		Spec: v1alpha1.ClusterLinkSpec{
			Cluster:    "3hfr4o24-web",
			Service:    v1alpha1.ClusterLinkApiserver,
			TargetPort: 20010,
		},
		Status: v1alpha1.ClusterLinkStatus{
			Phase:      v1alpha1.ClusterLinkListening,
			EndpointID: strings.Repeat("ab", 32),
			Did:        "did:key:z6MkiTBz1ymuepAQ4HEHYSF1H8quG5GLVVQR3djdX3mDooWp",
			Ticket:     "endpointaaaa",
		},
	}
}

func TestWriteClusterLinkCreatesItInTheHomeWorkspaceAndThenWritesItsStatus(t *testing.T) {
	link := listeningLink()
	f := &fakeKCP{t: t, respond: func(req request) (int, any) {
		switch {
		case req.Method == http.MethodGet && req.Path == linkPath(link.Name):
			return notFound()
		case req.Method == http.MethodPost && req.Path == "/clusters/"+homePath+"/apis/socialweb.computer/v1alpha1/clusterlinks":
			return http.StatusCreated, link
		case req.Method == http.MethodPatch && req.Path == linkPath(link.Name)+"/status":
			return http.StatusOK, link
		}
		return 0, nil
	}}
	c := newTestClient(t, f)
	if err := c.WriteClusterLink(context.Background(), homePath, link); err != nil {
		t.Fatal(err)
	}
	var posted, status *request
	for _, r := range f.seen() {
		switch {
		case r.Method == http.MethodPost:
			posted = &r
		case r.Method == http.MethodPatch && strings.HasSuffix(r.Path, "/status"):
			status = &r
		}
	}
	if posted == nil || status == nil {
		t.Fatalf("want a create and a status patch, saw %+v", f.seen())
	}
	body := posted.json(t)
	for field, want := range map[string]any{"cluster": "3hfr4o24-web", "service": "apiserver", "targetPort": float64(20010)} {
		if got := dig(t, body, "spec", field); got != want {
			t.Errorf("created spec.%s = %v, want %v", field, got, want)
		}
	}
	patch := status.json(t)
	for field, want := range map[string]any{
		"phase": "Listening", "endpointId": link.Status.EndpointID, "did": link.Status.Did, "ticket": "endpointaaaa",
	} {
		if got := dig(t, patch, "status", field); got != want {
			t.Errorf("status.%s = %v, want %v", field, got, want)
		}
	}
	if got := dig(t, patch, "status", "message"); got != nil {
		t.Errorf("a Listening link carries status.message %v; a stale failure message must be cleared", got)
	}
}

func TestWriteClusterLinkIsSilentWhenNothingChanged(t *testing.T) {
	link := listeningLink()
	f := &fakeKCP{t: t, respond: func(req request) (int, any) {
		if req.Method == http.MethodGet && req.Path == linkPath(link.Name) {
			return http.StatusOK, link
		}
		return 0, nil
	}}
	if err := newTestClient(t, f).WriteClusterLink(context.Background(), homePath, link); err != nil {
		t.Fatal(err)
	}
	if f.hasMethod(http.MethodPost) || f.hasMethod(http.MethodPatch) {
		t.Fatalf("an unchanged link was written: %+v", f.seen())
	}
}

func TestWriteClusterLinkStatusNeverRecreatesALinkTheTeardownDeleted(t *testing.T) {
	f := &fakeKCP{t: t, respond: func(req request) (int, any) {
		if req.Method == http.MethodPatch && req.Path == linkPath("gone-openbao")+"/status" {
			return notFound()
		}
		return 0, nil
	}}
	wrote, err := newTestClient(t, f).WriteClusterLinkStatus(context.Background(), homePath, "gone-openbao",
		v1alpha1.ClusterLinkStatus{Phase: v1alpha1.ClusterLinkFailed, Message: "exited"})
	if err != nil || wrote {
		t.Fatalf("WriteClusterLinkStatus = %v %v, want false nil", wrote, err)
	}
	if f.hasMethod(http.MethodPost) {
		t.Fatal("a status write created the link")
	}
}

func TestDeleteClusterLinkToleratesAnAbsentLink(t *testing.T) {
	f := &fakeKCP{t: t, respond: func(req request) (int, any) {
		if req.Method == http.MethodDelete && req.Path == linkPath("x-apiserver") {
			return notFound()
		}
		return 0, nil
	}}
	if err := newTestClient(t, f).DeleteClusterLink(context.Background(), homePath, "x-apiserver"); err != nil {
		t.Fatal(err)
	}
}

func TestWriteClusterLinkRefusesALinkWithNoPhase(t *testing.T) {
	link := listeningLink()
	link.Status.Phase = ""
	f := &fakeKCP{t: t, respond: func(request) (int, any) { return 0, nil }}
	if err := newTestClient(t, f).WriteClusterLink(context.Background(), homePath, link); err == nil {
		t.Fatal("a link with no phase was written")
	}
}
