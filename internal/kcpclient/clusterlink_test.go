package kcpclient

import (
	"context"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

func TestReadClusterLinkCarriesTheTicketAndReportsAbsence(t *testing.T) {
	var paths []string
	f := &fakeKCP{t: t, respond: func(req request) (int, any) {
		if req.Method != http.MethodGet {
			return 0, nil
		}
		paths = append(paths, req.Path)
		if len(paths) == 1 {
			return http.StatusOK, v1alpha1.ClusterLink{
				TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ClusterLink"},
				ObjectMeta: metav1.ObjectMeta{Name: "c0ffee-apiserver"},
				Status: v1alpha1.ClusterLinkStatus{
					Phase: v1alpha1.ClusterLinkListening, Ticket: "nodeticket",
				},
			}
		}
		return notFound()
	}}
	c := newTestClient(t, f)
	link, err := c.ReadClusterLink(context.Background(), homePath, "c0ffee-apiserver")
	if err != nil {
		t.Fatalf("ReadClusterLink: %v", err)
	}
	if link == nil || link.Status.Phase != v1alpha1.ClusterLinkListening || link.Status.Ticket != "nodeticket" {
		t.Fatalf("the link's status did not survive the read: %+v", link)
	}
	want := "/clusters/" + homePath + "/apis/" + v1alpha1.GroupVersion.String() + "/clusterlinks/c0ffee-apiserver"
	if paths[0] != want {
		t.Fatalf("the link was read from %s, want %s", paths[0], want)
	}
	missing, err := c.ReadClusterLink(context.Background(), homePath, "c0ffee-apiserver")
	if err != nil || missing != nil {
		t.Fatalf("an absent link read as %+v, %v; want nil and no error", missing, err)
	}
}
