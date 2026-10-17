package appctl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/devinstance"
)

const (
	VouchNSID = "sh.tangled.graph.vouch"

	OperatorRepo = "operator"

	operatorVouchPrefix = "operator-vouch-"
)

type operatorOf struct {
	app string
	did string
	at  string
}

func (n *networks) declareOperatorVouches(ctx context.Context, apps []v1alpha1.NetworkApp) {
	var operators []operatorOf
	var gateways []string
	for _, app := range apps {
		if app.Stopped {
			continue
		}
		c, err := n.child(app)
		if err != nil {
			continue
		}
		var rec devinstance.Record
		if devinstance.ReadJSON(c.layout.Record, &rec) != nil || rec.BaseWorkspace == "" {
			continue
		}
		switch app.Kind {
		case v1alpha1.ApplicationKindMarketBidder:
			id, err := n.ctl.identityIn(ctx, rec.BaseWorkspace, BidderIdentity)
			if err == nil && len(id.Spec.ApprovedAssociations) > 0 {
				operators = append(operators, operatorOf{app: app.Name, did: id.Spec.ApprovedAssociations[0],
					at: id.CreationTimestamp.UTC().Format("2006-01-02T15:04:05Z")})
			}
		case v1alpha1.ApplicationKindGateway:
			gateways = append(gateways, rec.BaseWorkspace)
		}
	}
	for _, ws := range gateways {
		for _, op := range operators {
			if err := n.ctl.applyOperatorVouch(ctx, ws, op); err != nil {
				n.ctl.c.log().Warn("declaring the gateway operator's vouch for a bidder's operator", "workspace", ws, "bidder", op.app, "err", err.Error())
			}
		}
	}
}

func (ctl *Controller) applyOperatorVouch(ctx context.Context, ws string, op operatorOf) error {
	ri, err := ctl.client(ws, AtprotoRecordGVR)
	if err != nil {
		return err
	}
	record, err := json.Marshal(map[string]any{"$type": VouchNSID, "subject": op.did, "createdAt": op.at})
	if err != nil {
		return err
	}
	obj := v1alpha1.AtprotoRecord{ObjectMeta: metav1.ObjectMeta{Name: operatorVouchPrefix + op.app},
		Spec: v1alpha1.AtprotoRecordSpec{Repo: OperatorRepo, Collection: VouchNSID, Rkey: op.did, Record: record}}
	u, err := toUnstructured(obj, "AtprotoRecord")
	if err != nil {
		return err
	}
	delete(u.Object, "status")
	if md, ok := u.Object["metadata"].(map[string]any); ok {
		delete(md, "creationTimestamp")
	}
	body, err := u.MarshalJSON()
	if err != nil {
		return err
	}
	force := true
	_, err = ri.Patch(ctx, u.GetName(), types.ApplyPatchType, body, metav1.PatchOptions{FieldManager: FieldManager, Force: &force})
	return err
}

// ponytail: the operator's records are written and kept, never deleted when their object goes; the market network's PDS goes with the network, so add a finalizer when an operator repo outlives one.
func (ctl *Controller) reconcileOperatorRecords(ctx context.Context, ws, sessionsFile string, n *v1alpha1.MarketNetworkValue) error {
	if n == nil {
		return nil
	}
	ri, err := ctl.client(ws, AtprotoRecordGVR)
	if err != nil {
		return err
	}
	list, err := ri.List(ctx, metav1.ListOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var held []v1alpha1.AtprotoRecord
	for i := range list.Items {
		var o v1alpha1.AtprotoRecord
		if err := fromUnstructured(&list.Items[i], &o); err != nil {
			return err
		}
		if o.Spec.Repo == OperatorRepo && o.DeletionTimestamp == nil &&
			(o.Status.Phase != v1alpha1.AtprotoRecordPhaseApplied || o.Status.ObservedGeneration != o.Generation) {
			held = append(held, o)
		}
	}
	if len(held) == 0 {
		return nil
	}
	b, err := os.ReadFile(sessionsFile)
	if err != nil {
		return err
	}
	var sessions map[string]ownerSession
	if err := json.Unmarshal(b, &sessions); err != nil {
		return fmt.Errorf("the owner sessions file %s: %w", sessionsFile, err)
	}
	owners := make([]string, 0, len(sessions))
	for did := range sessions {
		owners = append(owners, did)
	}
	if len(owners) == 0 {
		return fmt.Errorf("the owner sessions file %s holds no session", sessionsFile)
	}
	slices.Sort(owners)
	s := sessions[owners[0]]
	pds := strings.TrimSuffix(s.Service, "/")
	sess, err := xrpcCall(ctx, http.MethodPost, pds+"/xrpc/com.atproto.server.createSession", n.CaBundleRef, "",
		map[string]string{"identifier": s.Identifier, "password": s.Password})
	if err != nil {
		return err
	}
	operator, _ := sess["did"].(string)
	access, _ := sess["accessJwt"].(string)
	for _, o := range held {
		var record map[string]any
		if err := json.Unmarshal(o.Spec.Record, &record); err != nil {
			return fmt.Errorf("AtprotoRecord %s: the record is not a JSON object: %w", o.Name, err)
		}
		out, err := xrpcCall(ctx, http.MethodPost, pds+"/xrpc/com.atproto.repo.putRecord", n.CaBundleRef, access,
			map[string]any{"repo": operator, "collection": o.Spec.Collection, "rkey": o.Spec.Rkey, "record": record})
		if err != nil {
			return fmt.Errorf("AtprotoRecord %s: %w", o.Name, err)
		}
		uri, _ := out["uri"].(string)
		cid, _ := out["cid"].(string)
		status, err := json.Marshal(map[string]any{"status": map[string]any{"phase": v1alpha1.AtprotoRecordPhaseApplied,
			"observedGeneration": o.Generation, "did": operator, "uri": uri, "cid": cid, "message": ""}})
		if err != nil {
			return err
		}
		if _, err := ri.Patch(ctx, o.Name, types.MergePatchType, status, metav1.PatchOptions{}, "status"); err != nil {
			return err
		}
		ctl.c.log().Info("the market network operator account wrote a declared record", "record", o.Name, "uri", uri,
			"collection", o.Spec.Collection, "rkey", o.Spec.Rkey)
	}
	return nil
}
