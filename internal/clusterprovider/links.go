package clusterprovider

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterlink"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/materialiser"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/portallocator"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/processrunner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

var LinkPurposes = []portallocator.Purpose{portallocator.PurposeApiserverLink, portallocator.PurposeOpenBaoLink}

const linkFailureTail = 600

const DefaultLinkRetry = 2 * time.Second

var ErrNoLinkSeam = errors.New(
	"clusterprovider: cluster links are on and this Stack runs no vault or no k3s. A link's iroh " +
		"key lives in the cluster's own OpenBao, so with no vault there is nowhere to keep it and " +
		"every restart would mint a new endpoint id; with no k3s there is no apiserver to front")

var ErrNoLinkRecorder = errors.New(
	"clusterprovider: cluster links are on and nothing records them. The ClusterLink is the " +
		"only place a link's ticket and did:key are published, so a listener nobody records is " +
		"one no node can dial")

type LinkRecorder interface {
	WriteLink(ctx context.Context, link v1alpha1.ClusterLink) error

	WriteLinkStatus(ctx context.Context, cluster, name string, status v1alpha1.ClusterLinkStatus) error
}

type LinkKeys func(ctx context.Context, dataDir string, vaultPort int) (clusterlink.KeyStore, error)

type Links struct {
	Dumbpipe string

	Recorder LinkRecorder

	Live func(cluster string) bool

	Keys LinkKeys

	Random io.Reader

	Retry time.Duration

	Log *slog.Logger
}

type linkTarget struct {
	component procgroup.Component
	service   v1alpha1.ClusterLinkService
	purpose   portallocator.Purpose
	port      int
}

func (cfg StackConfig) WithoutUnleasedLinks(l materialiser.Leases) StackConfig {
	if cfg.Links == nil {
		return cfg
	}
	for _, p := range LinkPurposes {
		if _, ok := l[p]; !ok {
			cfg.Links = nil
			return cfg
		}
	}
	return cfg
}

func linksAfterK3s(steps []procgroup.Step, cfg StackConfig, cluster, dataDir string, l materialiser.Leases, apiserverPort, vaultPort int) ([]procgroup.Step, error) {
	links := cfg.Links
	if !cfg.Stack.Has(procgroup.ComponentOpenBao) || !cfg.Stack.Has(procgroup.ComponentK3s) || vaultPort == 0 {
		return nil, fmt.Errorf("%w (stack %v, cluster %s)", ErrNoLinkSeam, cfg.Stack, cluster)
	}
	if links.Recorder == nil {
		return nil, fmt.Errorf("%w (cluster %s)", ErrNoLinkRecorder, cluster)
	}
	if links.Dumbpipe == "" {
		return nil, fmt.Errorf("clusterprovider: cluster links are on and no dumbpipe binary is named (cluster %s)", cluster)
	}
	keys := links.Keys
	if keys == nil {
		keys = VaultLinkKeys
	}
	random := links.Random
	if random == nil {
		random = rand.Reader
	}
	targets := []linkTarget{
		{procgroup.ComponentApiserverLink, v1alpha1.ClusterLinkApiserver, portallocator.PurposeApiserverLink, apiserverPort},
		{procgroup.ComponentOpenBaoLink, v1alpha1.ClusterLinkOpenBao, portallocator.PurposeOpenBaoLink, vaultPort},
	}
	for _, t := range targets {
		linkPort, err := leasePort(l, t.purpose, cluster)
		if err != nil {
			return nil, err
		}
		output, err := componentLog(dataDir, t.component)
		if err != nil {
			return nil, err
		}
		run := &linkRun{
			links:   links,
			cluster: cluster,
			target:  t,
			keys:    func(ctx context.Context) (clusterlink.KeyStore, error) { return keys(ctx, dataDir, vaultPort) },
			random:  random,
		}
		steps = append(steps, procgroup.Step{
			Name:    t.component,
			Spec:    processrunner.Spec{Argv: clusterlink.Argv(links.Dumbpipe, t.port, linkPort), Output: output},
			Prepare: run.prepare,
			Ready:   run.ready,
		})
	}
	return steps, nil
}

type linkRun struct {
	links   *Links
	cluster string
	target  linkTarget
	keys    func(ctx context.Context) (clusterlink.KeyStore, error)
	random  io.Reader

	mu  sync.Mutex
	key clusterlink.Key
}

func (r *linkRun) name() string { return clusterlink.Name(r.cluster, r.target.service) }

func (r *linkRun) log() *slog.Logger {
	if r.links.Log != nil {
		return r.links.Log
	}
	return slog.Default()
}

func (r *linkRun) prepare(ctx context.Context, spec processrunner.Spec) (processrunner.Spec, error) {
	store, err := r.keys(ctx)
	if err != nil {
		return spec, fmt.Errorf("clusterprovider: the %s link of %s keeps its key in the cluster vault: %w",
			r.target.service, r.cluster, err)
	}
	key, err := clusterlink.EnsureKey(ctx, store, r.target.service, r.random)
	if err != nil {
		return spec, err
	}
	r.mu.Lock()
	r.key = key
	r.mu.Unlock()
	env := spec.Env
	if env == nil {
		env = os.Environ()
	}
	spec.Env = append(append([]string{}, env...), key.Env())
	return spec, nil
}

func (r *linkRun) currentKey() clusterlink.Key {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.key
}

func (r *linkRun) ready(ctx context.Context, p *processrunner.Process) error {
	ctx, cancel := context.WithTimeout(ctx, GateTimeout)
	defer cancel()
	key := r.currentKey()
	exited := make(chan error, 1)
	go func() { exited <- p.Wait() }()
	tick := time.NewTicker(PollInterval)
	defer tick.Stop()
	for {
		if ticket, ok := clusterlink.TicketIn(p.Output()); ok {
			status, err := clusterlink.Listening(key, ticket)
			if err != nil {
				return err
			}
			go r.report(status, exited, p)
			return nil
		}
		select {
		case err := <-exited:
			return fmt.Errorf("clusterprovider: the %s link of %s exited before it printed a ticket (%v): %s",
				r.target.service, r.cluster, err, tail(p.Output()))
		case <-ctx.Done():
			return &errNotReady{
				what: "the " + string(r.target.service) + " link of " + r.cluster + ". dumbpipe prints " +
					"its ticket once its endpoint is bound, after one try at its home relay",
				last: tail(p.Output()),
			}
		case <-tick.C:
		}
	}
}

func (r *linkRun) report(listening v1alpha1.ClusterLinkStatus, exited <-chan error, p *processrunner.Process) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	link := clusterlink.Object(r.cluster, r.target.service, r.target.port, listening)
	written := make(chan struct{})
	go func() {
		defer close(written)
		r.retry(ctx, "Listening", func() error { return r.links.Recorder.WriteLink(ctx, link) })
	}()
	err := <-exited
	cancel()
	<-written
	key := r.currentKey()
	status := clusterlink.Failed(key, fmt.Sprintf("dumbpipe exited (%v): %s", err, tail(p.Output())))
	if r.links.Live != nil && !r.links.Live(r.cluster) {
		status = v1alpha1.ClusterLinkStatus{
			Phase:      v1alpha1.ClusterLinkPending,
			EndpointID: key.EndpointID,
			Did:        key.Did,
			Message:    "the cluster's process group was stopped",
		}
	}
	final, done := context.WithTimeout(context.Background(), GateTimeout)
	defer done()
	r.retry(final, string(status.Phase), func() error {
		return r.links.Recorder.WriteLinkStatus(final, r.cluster, r.name(), status)
	})
}

func (r *linkRun) retry(ctx context.Context, phase string, write func() error) {
	wait := r.links.Retry
	if wait <= 0 {
		wait = DefaultLinkRetry
	}
	for {
		err := write()
		if err == nil {
			r.log().Info("cluster provider: cluster link recorded", "cluster", r.cluster,
				"link", r.name(), "phase", phase)
			return
		}
		r.log().Warn("cluster provider: recording the cluster link failed; retrying",
			"cluster", r.cluster, "link", r.name(), "phase", phase, "error", err.Error())
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func tail(out string) string {
	out = strings.TrimSpace(out)
	if len(out) > linkFailureTail {
		out = out[len(out)-linkFailureTail:]
	}
	return out
}

type vaultLinkKeys struct {
	client *openbao.Client
	admin  *vaultAdmin
}

func VaultLinkKeys(ctx context.Context, dataDir string, vaultPort int) (clusterlink.KeyStore, error) {
	seal, err := ReadVaultSeal(dataDir)
	if err != nil {
		return nil, err
	}
	pool, err := vaultCA(dataDir)
	if err != nil {
		return nil, err
	}
	addr := VaultAddr(vaultPort)
	store := &vaultLinkKeys{
		client: openbao.New(addr, seal.RootToken, openbao.WithCACertPool(pool)),
		admin:  newVaultAdmin(addr, seal.RootToken, pool),
	}
	exists, err := store.admin.mountExists(ctx, clusterlink.Mount)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := store.client.EnableKV(ctx, clusterlink.Mount, 2); err != nil {
			return nil, fmt.Errorf("clusterprovider: enabling the %s KV v2 mount in the cluster vault: %w",
				clusterlink.Mount, err)
		}
	}
	return store, nil
}

func linkDataPath(path string) string { return clusterlink.Mount + "/data/" + strings.Trim(path, "/") }

func kvUpgrading(body string) bool { return strings.Contains(strings.ToLower(body), "upgrad") }

func (s *vaultLinkKeys) Read(ctx context.Context, path string) (map[string]any, bool, error) {
	var out map[string]any
	var found bool
	err := s.untilMounted(ctx, func() (bool, error) {
		data, err := s.client.GetKV(ctx, linkDataPath(path))
		var re *openbao.ResponseError
		switch {
		case errors.Is(err, openbao.ErrNotFound):
			return true, nil
		case errors.As(err, &re) && kvUpgrading(re.Body):
			return false, nil
		case err != nil:
			return false, err
		}
		out, found = data, true
		return true, nil
	})
	return out, found, err
}

func (s *vaultLinkKeys) Create(ctx context.Context, path string, data map[string]any) (bool, error) {
	var created bool
	err := s.untilMounted(ctx, func() (bool, error) {
		status, body, err := s.admin.do(ctx, http.MethodPost, linkDataPath(path), map[string]any{
			"options": map[string]any{"cas": 0},
			"data":    data,
		})
		switch {
		case err != nil:
			return false, err
		case status >= 200 && status <= 299:
			created = true
			return true, nil
		case kvUpgrading(string(body)):
			return false, nil
		case status == http.StatusBadRequest && strings.Contains(string(body), "check-and-set"):
			return true, nil
		}
		return false, fmt.Errorf("clusterprovider: POST /v1/%s -> HTTP %d: %s", linkDataPath(path), status,
			strings.TrimSpace(string(body)))
	})
	return created, err
}

func (s *vaultLinkKeys) untilMounted(ctx context.Context, try func() (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, GateTimeout)
	defer cancel()
	return poll(ctx, func(context.Context) (bool, error) { return try() }, func() error {
		return fmt.Errorf("clusterprovider: the %s KV v2 mount never finished its upgrade to versioned data",
			clusterlink.Mount)
	})
}
