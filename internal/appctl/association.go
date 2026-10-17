package appctl

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
)

const (
	AssociateConfirmNSID = "com.publicdomainrelay.temp.requester.associateConfirm"
	BadgeBlueKeysNSID    = "com.publicdomainrelay.temp.badgeBlueKeys"
	BidderAssociate      = "bidder_associate"
)

func xrpcCall(ctx context.Context, method, rawURL, caFile, bearer string, body any) (map[string]any, error) {
	tr := &http.Transport{}
	if caFile != "" && strings.HasPrefix(rawURL, "https://") {
		caPEM, err := os.ReadFile(caFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("the CA bundle %s does not parse", caFile)
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool}
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second, Transport: tr}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out map[string]any
	json.Unmarshal(raw, &out)
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("%s %s: %d %s", method, rawURL, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return out, nil
}

func (ctl *Controller) associateNetworkOperator(ctx context.Context, spec v1alpha1.MarketBidderSpec, pdsAddress string) error {
	id, err := ctl.bidderIdentity(ctx)
	if err != nil || id.Status.Phase != v1alpha1.MarketIdentityPhaseAwaitingScan || id.Status.Did == "" || pdsAddress == "" {
		return err
	}
	password, _, err := ctl.bidderPassword(ctx)
	if err != nil {
		return err
	}
	pds := strings.TrimSuffix(spec.Identity.PdsUrl, "/")
	ca := spec.Network.CaBundleRef
	sess, err := xrpcCall(ctx, http.MethodPost, pds+"/xrpc/com.atproto.server.createSession", ca, "",
		map[string]string{"identifier": spec.Identity.Handle, "password": password})
	if err != nil {
		return err
	}
	operator, _ := sess["did"].(string)
	access, _ := sess["accessJwt"].(string)
	q := url.Values{"aud": {id.Status.Did}, "lxm": {AssociateConfirmNSID}}
	sa, err := xrpcCall(ctx, http.MethodGet, pds+"/xrpc/com.atproto.server.getServiceAuth?"+q.Encode(), ca, access, nil)
	if err != nil {
		return err
	}
	token, _ := sa["token"].(string)
	if _, err := xrpcCall(ctx, http.MethodPost, "http://"+pdsAddress+"/xrpc/"+AssociateConfirmNSID, "", token, map[string]any{}); err != nil {
		return err
	}
	rec := map[string]any{"$type": BadgeBlueKeysNSID, "keyId": id.Status.Did, "challenge": operator, "service": BidderAssociate,
		"name": "bidder-" + id.Status.Did[len(id.Status.Did)-8:], "createdAt": time.Now().UTC().Format(time.RFC3339)}
	if _, err := xrpcCall(ctx, http.MethodPost, pds+"/xrpc/com.atproto.repo.createRecord", ca, access,
		map[string]any{"repo": operator, "collection": BadgeBlueKeysNSID, "record": rec}); err != nil {
		return err
	}
	ctl.c.log().Info("the market network operator account associated the bidder's own DID", "operator", operator,
		"handle", spec.Identity.Handle, "bidderDid", id.Status.Did)
	return nil
}
