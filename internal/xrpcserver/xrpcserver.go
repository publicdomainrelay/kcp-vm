package xrpcserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/bluesky-social/indigo/atproto/identity"
	"github.com/bluesky-social/indigo/atproto/lexicon"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/atprotoauth"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/gateway"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/mint"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/revocation"
)

const (
	XRPCPrefix = "/xrpc/"

	MintPath = "/v1/credential"

	NSIDPrefix = "com.publicdomainrelay.temp.cluster."

	maxBody = 1 << 16

	DefaultRetryAfter = 5 * time.Second
)

const (
	nsidGetKubeconfig    = NSIDPrefix + "getKubeconfig"
	nsidCreateCluster    = NSIDPrefix + "createCluster"
	nsidControlPlaneUp   = NSIDPrefix + "controlPlaneUp"
	nsidControlPlaneDown = NSIDPrefix + "controlPlaneDown"
	nsidDeleteCluster    = NSIDPrefix + "deleteCluster"
	nsidListClusters     = NSIDPrefix + "listClusters"
	nsidRevokeIdentity   = NSIDPrefix + "revokeIdentity"
)

var retryable = map[string]bool{gateway.ErrClusterInitializing.Name: true}

type Options struct {
	Gateway *gateway.Server

	Mint *mint.Mint

	Audiences []string

	Directory identity.Directory

	RetryAfter time.Duration
}

type verb func(ctx context.Context, caller gateway.Caller, in any) (any, error)

type route struct {
	nsid  string
	query bool

	errors []lexicon.SchemaError

	input any

	call verb
}

type Server struct {
	gateway *gateway.Server
	mint    http.Handler
	opts    Options
	routes  []route
	catalog *lexicon.BaseCatalog
	inputs  map[string]lexicon.SchemaObject
	params  map[string]lexicon.SchemaObject
}

func New(opts Options) (*Server, error) {
	if opts.Gateway == nil {
		return nil, errors.New("xrpcserver: no gateway")
	}
	if opts.Mint == nil {
		return nil, errors.New("xrpcserver: no mint")
	}
	if len(opts.Audiences) == 0 {
		return nil, errors.New("xrpcserver: no audiences; every token would be refused as Unauthorized")
	}
	if opts.Directory == nil {
		return nil, errors.New("xrpcserver: no directory; did:plc and did:web issuers could not be resolved")
	}
	if opts.RetryAfter == 0 {
		opts.RetryAfter = DefaultRetryAfter
	}

	cat, err := loadCatalog()
	if err != nil {
		return nil, err
	}

	s := &Server{
		gateway: opts.Gateway,
		mint:    mintHandler{m: opts.Mint},
		opts:    opts,
		catalog: cat,
		inputs:  map[string]lexicon.SchemaObject{},
		params:  map[string]lexicon.SchemaObject{},
	}

	for _, r := range []route{
		{nsid: nsidGetKubeconfig, input: &gateway.KubeconfigRequest{},
			call: func(ctx context.Context, c gateway.Caller, in any) (any, error) {
				return s.gateway.GetKubeconfig(ctx, c, *(in.(*gateway.KubeconfigRequest)))
			}},
		{nsid: nsidCreateCluster, call: func(ctx context.Context, c gateway.Caller, in any) (any, error) {
			return s.gateway.CreateCluster(ctx, c, *(in.(*gateway.NameRequest)))
		}},
		{nsid: nsidControlPlaneUp, call: func(ctx context.Context, c gateway.Caller, in any) (any, error) {
			return s.gateway.ControlPlaneUp(ctx, c, *(in.(*gateway.NameRequest)))
		}},
		{nsid: nsidControlPlaneDown, call: func(ctx context.Context, c gateway.Caller, in any) (any, error) {
			return s.gateway.ControlPlaneDown(ctx, c, *(in.(*gateway.NameRequest)))
		}},
		{nsid: nsidDeleteCluster, call: func(ctx context.Context, c gateway.Caller, in any) (any, error) {
			return s.gateway.DeleteCluster(ctx, c, *(in.(*gateway.NameRequest)))
		}},
		{nsid: nsidListClusters, call: func(ctx context.Context, c gateway.Caller, _ any) (any, error) {
			return s.gateway.ListClusters(ctx, c)
		}},
		{nsid: nsidRevokeIdentity, input: &gateway.RevokeRequest{},
			call: func(ctx context.Context, c gateway.Caller, in any) (any, error) {
				return s.gateway.RevokeIdentity(ctx, c, *(in.(*gateway.RevokeRequest)))
			}},
	} {
		if err := s.bind(&r); err != nil {
			return nil, err
		}
		s.routes = append(s.routes, r)
	}
	return s, nil
}

func (s *Server) bind(r *route) error {
	sch, err := s.catalog.Resolve(r.nsid)
	if err != nil {
		return fmt.Errorf("xrpcserver: %s has no lexicon: %w", r.nsid, err)
	}
	switch def := sch.Def.(type) {
	case lexicon.SchemaProcedure:
		if def.Input == nil {
			return fmt.Errorf("xrpcserver: %s is a procedure with no declared input", r.nsid)
		}
		obj, err := objectOf(r.nsid+" input", def.Input.Schema)
		if err != nil {
			return err
		}
		s.inputs[r.nsid] = obj
		r.errors = def.Errors
	case lexicon.SchemaQuery:
		s.params[r.nsid] = paramsAsObject(def.Parameters)
		r.query = true
		r.errors = def.Errors
	default:
		return fmt.Errorf("xrpcserver: %s is a %T; only procedures and queries are served", r.nsid, sch.Def)
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	for _, r := range s.routes {
		method := http.MethodPost
		if r.query {
			method = http.MethodGet
		}
		mux.Handle(method+" "+XRPCPrefix+r.nsid, s.xrpc(r))
	}
	mux.Handle(http.MethodPost+" "+MintPath, s.mint)
	return mux
}

func (s *Server) xrpc(r route) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		caller, ok := s.authorize(w, req, r.nsid)
		if !ok {
			return
		}

		in := any(&gateway.NameRequest{})
		if r.query {
			params, err := queryParams(req.URL.Query(), s.params[r.nsid])
			if err != nil {
				writeError(w, r.nsid, s.opts.RetryAfter, gateway.ErrInvalidRequest)
				return
			}
			if err := validate(r.nsid+" parameters", s.catalog, s.params[r.nsid], params); err != nil {
				writeError(w, r.nsid, s.opts.RetryAfter, gateway.ErrInvalidRequest)
				return
			}
		} else {
			body, err := decodeBody(req, r.nsid, s.catalog, s.inputs[r.nsid], r.input)
			if err != nil {
				writeError(w, r.nsid, s.opts.RetryAfter, gateway.ErrInvalidRequest)
				return
			}
			in = body
		}

		out, err := r.call(req.Context(), caller, in)
		if err != nil {
			writeError(w, r.nsid, s.opts.RetryAfter, err)
			return
		}
		writeJSON(w, http.StatusOK, shape(r.nsid, out))
	}
}

func (s *Server) authorize(w http.ResponseWriter, req *http.Request, nsid string) (gateway.Caller, bool) {
	token, ok := bearer(req.Header.Get("Authorization"))
	if !ok {
		writeError(w, nsid, s.opts.RetryAfter, gateway.ErrUnauthorized)
		return "", false
	}

	did, err := atprotoauth.Verify(req.Context(), token, nsid, s.opts.Audiences, s.opts.Directory)
	if err != nil {
		if errors.Is(err, atprotoauth.ErrNoKey) {
			writeError(w, nsid, s.opts.RetryAfter, gateway.ErrClusterUnavailable)
			return "", false
		}
		writeError(w, nsid, s.opts.RetryAfter, gateway.ErrUnauthorized)
		return "", false
	}

	return gateway.Caller(did), true
}

func decodeBody(req *http.Request, nsid string, cat *lexicon.BaseCatalog, def lexicon.SchemaObject, target any) (any, error) {
	out := target
	if out == nil {
		out = &gateway.NameRequest{}
	}

	raw, err := io.ReadAll(io.LimitReader(req.Body, maxBody))
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var body map[string]any
	if err := dec.Decode(&body); err != nil {
		return nil, err
	}
	if err := validate(nsid, cat, def, numbersAsInts(body)); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, err
	}
	if err := checkClusterName(out); err != nil {
		return nil, err
	}
	return out, nil
}

func checkClusterName(out any) error {
	switch t := out.(type) {
	case *gateway.NameRequest:
		return labelOrInvalidRequest(t.Name)
	case *gateway.KubeconfigRequest:
		return labelOrInvalidRequest(t.Name)
	case *gateway.RevokeRequest:
		if t.Name == "" {
			return nil
		}
		return labelOrInvalidRequest(t.Name)
	}
	return nil
}

func labelOrInvalidRequest(name string) error {
	if len(validation.IsDNS1123Label(name)) > 0 {
		return gateway.ErrInvalidRequest
	}
	return nil
}

func numbersAsInts(v any) map[string]any {
	m, ok := v.(map[string]any)
	if !ok {
		return map[string]any{}
	}
	for k, e := range m {
		m[k] = numberAsInt(e)
	}
	return m
}

func numberAsInt(v any) any {
	switch t := v.(type) {
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	case map[string]any:
		for k, e := range t {
			t[k] = numberAsInt(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = numberAsInt(e)
		}
		return t
	default:
		return v
	}
}

func queryParams(q url.Values, def lexicon.SchemaObject) (map[string]any, error) {
	out := map[string]any{}
	for name, sd := range def.Properties {
		vals, ok := q[name]
		if !ok || len(vals) == 0 {
			continue
		}
		switch sd.Inner.(type) {
		case lexicon.SchemaInteger:
			n, err := strconv.ParseInt(vals[0], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("xrpcserver: %s is not an integer: %w", name, err)
			}
			out[name] = n
		case lexicon.SchemaBoolean:
			b, err := strconv.ParseBool(vals[0])
			if err != nil {
				return nil, fmt.Errorf("xrpcserver: %s is not a boolean: %w", name, err)
			}
			out[name] = b
		default:
			out[name] = vals[0]
		}
	}
	return out, nil
}

func shape(nsid string, out any) any {
	switch v := out.(type) {
	case *gateway.GetKubeconfigResponse:
		return getKubeconfigOutput{Kubeconfig: v.Kubeconfig, Instance: v.Instance}
	case gateway.ClusterView:
		return phaseOutput{Phase: v.Phase}
	case *gateway.RevokeResponse:
		return revokeIdentityOutput{Revoked: v.Revoked}
	case []gateway.ClusterView:
		items := make([]clusterItem, 0, len(v))
		for _, c := range v {
			items = append(items, clusterItem{
				Name:            c.Name,
				Phase:           c.Phase,
				Running:         c.Running,
				Instance:        c.Instance,
				TeardownBlocked: c.TeardownBlocked,
				Server:          c.URL,
			})
		}
		return listClustersOutput{Clusters: items}
	default:
		return out
	}
}

type getKubeconfigOutput struct {
	Kubeconfig string `json:"kubeconfig"`
	Instance   string `json:"instance"`
}

type phaseOutput struct {
	Phase string `json:"phase"`
}

type revokeIdentityOutput struct {
	Revoked []revocation.ReplyEntry `json:"revoked"`
}

type clusterItem struct {
	Name            string `json:"name"`
	Phase           string `json:"phase"`
	Running         bool   `json:"running"`
	Instance        string `json:"instance"`
	TeardownBlocked bool   `json:"teardownBlocked,omitempty"`
	Server          string `json:"server"`
}

type listClustersOutput struct {
	Clusters []clusterItem `json:"clusters"`
}

func writeError(w http.ResponseWriter, nsid string, retryAfter time.Duration, err error) {
	var xe *gateway.XRPCError
	if !errors.As(err, &xe) {
		slog.Error("xrpc: unmapped error", "nsid", nsid, "err", err)
		xe = &gateway.XRPCError{
			Name:    "InternalError",
			Message: "the request could not be completed",
			Status:  500,
		}
	}
	if retryable[xe.Name] {
		if retryAfter <= 0 {
			retryAfter = DefaultRetryAfter
		}
		w.Header().Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
	}
	writeJSON(w, xe.Status, errorBody{Error: xe.Name, Message: xe.Message})
}

type errorBody struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("xrpc: writing the response", "err", err)
	}
}

func bearer(h string) (string, bool) {
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(prefix):])
	return tok, tok != ""
}

type mintHandler struct {
	m *mint.Mint
}

func (h mintHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	token, ok := bearer(req.Header.Get("Authorization"))
	if !ok {
		writeError(w, MintPath, 0, gateway.ErrUnauthorized)
		return
	}

	cred, err := h.m.Issue(req.Context(), mint.DeviceCredential{Token: token}, time.Now())
	if err != nil {
		writeMintError(w, h.m, err)
		return
	}
	writeJSON(w, http.StatusOK, cred)
}

func writeMintError(w http.ResponseWriter, m *mint.Mint, err error) {
	switch {
	case errors.Is(err, mint.ErrBindingUnknown):
		writeError(w, MintPath, 0, gateway.ErrUnauthorized)
	case errors.Is(err, mint.ErrServiceAccountGone):
		writeError(w, MintPath, 0, gateway.ErrIdentityRevoked)
	case errors.Is(err, mint.ErrHeld):
		hold := m.Opts.FailureHold
		if hold <= 0 {
			hold = mint.DefaultFailureHold
		}
		w.Header().Set("Retry-After", strconv.Itoa(int(hold.Seconds())))
		writeJSON(w, http.StatusTooManyRequests, errorBody{
			Error:   "TooManyRequests",
			Message: "this credential failed recently and is in its failure hold",
		})
	default:
		writeError(w, MintPath, 0, err)
	}
}
