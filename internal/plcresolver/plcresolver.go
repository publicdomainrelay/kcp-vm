package plcresolver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/syntax"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
)

type Options struct {
	URL string

	Client *http.Client
}

type Resolver struct {
	dir *identity.BaseDirectory
}

type nxdomainIsAnOutage struct{ inner http.RoundTripper }

func (t nxdomainIsAnOutage) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(r)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil, fmt.Errorf(
			"the name does not resolve, which is an outage and not an absence: %s", err)
	}
	return resp, err
}

func New(opts Options) (*Resolver, error) {
	if opts.URL == "" {
		return nil, errors.New("plcresolver: URL is required")
	}
	if opts.Client == nil {
		return nil, errors.New("plcresolver: Client is required")
	}
	client := *opts.Client
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = nxdomainIsAnOutage{inner: transport}

	return &Resolver{dir: &identity.BaseDirectory{
		PLCURL:     strings.TrimSuffix(opts.URL, "/"),
		PLCClient:  &client,
		HTTPClient: client,
	}}, nil
}

func (r *Resolver) Resolve(ctx context.Context, did string) (revocation.PLCResolution, error) {
	parsed, err := syntax.ParseDID(did)
	if err != nil {
		return unknown(fmt.Errorf("plcresolver: %q is not a DID: %w", did, err))
	}
	switch parsed.Method() {
	case "plc", "web":
	default:
		return unknown(fmt.Errorf(
			"plcresolver: %s is a did:%s, and this package resolves only a method with a "+
				"document somewhere: a did:key's key IS its identity, so there is nothing to "+
				"resolve and nothing that could ever be found gone", did, parsed.Method()))
	}

	doc, err := r.dir.ResolveDID(ctx, parsed)
	switch {
	case errors.Is(err, identity.ErrDIDNotFound):
		return revocation.PLCResolution{Found: false}, nil
	case err != nil:
		return unknown(fmt.Errorf("plcresolver: resolve %s: %w", did, err))
	}

	keys := make([]string, 0, len(doc.VerificationMethod))
	for _, vm := range doc.VerificationMethod {
		keys = append(keys, vm.ID)
	}
	return revocation.PLCResolution{Found: true, Keys: keys}, nil
}

func unknown(err error) (revocation.PLCResolution, error) {
	return revocation.PLCResolution{Err: err}, err
}
