package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/common/denospec"
)

const (
	ProjectedDirName              = "projected"
	DefaultProjectedTokenSeconds  = int64(3600)
	ProjectedSourceCheckInterval  = 30 * time.Second
	ProjectedRetryInterval        = 5 * time.Second
	ProjectedOpenBaoCAFile        = "ca.crt"
	ProjectedOpenBaoCertFile      = "tls.crt"
	ProjectedOpenBaoKeyFile       = "tls.key"
	projectedTokenRefreshFraction = 2
)

type OpenBaoClientFiles struct {
	CA string

	Cert string

	Key string
}

type ProjectedSources interface {
	OpenBaoClient(ctx context.Context, workspace, server, client string) (OpenBaoClientFiles, error)
}

type projectedEntry struct {
	spec string

	updatedAt time.Time

	expiresAt time.Time

	checkedAt time.Time

	message string
}

type projectedStore struct {
	mu sync.Mutex

	pods map[string]map[string]*projectedEntry
}

type projection struct {
	env map[string]string

	reads []string

	status []v1alpha1.DenoPodProjected
}

func (p *Provider) projectedRoot() string {
	return filepath.Join(filepath.Dir(p.opts.RunsDir), ProjectedDirName)
}

func (p *Provider) projectedDir(ref Ref) string {
	ns := ref.Namespace
	if ns == "" {
		ns = "default"
	}
	return filepath.Join(p.projectedRoot(), strings.ReplaceAll(ref.LogicalCluster, ":", "_"), ns, ref.Name)
}

func (s *projectedStore) entries(key string) map[string]*projectedEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pods == nil {
		s.pods = map[string]map[string]*projectedEntry{}
	}
	m, ok := s.pods[key]
	if !ok {
		m = map[string]*projectedEntry{}
		s.pods[key] = m
	}
	return m
}

func (s *projectedStore) forget(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pods, key)
}

func (p *Provider) project(ctx context.Context, ref Ref, pod *v1alpha1.DenoPod) (projection, error) {
	out := projection{env: map[string]string{}}
	if len(pod.Spec.Projected) == 0 {
		return out, nil
	}
	if err := v1alpha1.CheckProjected(pod.Spec.Projected); err != nil {
		return out, err
	}
	dir := p.projectedDir(ref)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return out, fmt.Errorf("projected: %w", err)
	}
	have := p.projected.entries(ref.Key())
	now := p.opts.Now()
	var firstErr error
	keep := map[string]bool{}
	for _, c := range pod.Spec.Projected {
		keep[c.Name] = true
		raw, _ := json.Marshal(c)
		e, ok := have[c.Name]
		if !ok || e.spec != string(raw) {
			e = &projectedEntry{spec: string(raw)}
			have[c.Name] = e
		}
		path := filepath.Join(dir, c.Name)
		if p.projectedDue(c, e, now) {
			e.checkedAt = now
			changed, expires, err := p.writeProjected(ctx, ref, c, path)
			switch {
			case err != nil:
				e.message = err.Error()
				if firstErr == nil {
					firstErr = fmt.Errorf("projected %s: %w", c.Name, err)
				}
				p.opts.Log.Warn("projected: a credential of a DenoPod was not written", "pod", ref.Key(), "name", c.Name, "err", err.Error())
			default:
				e.message = ""
				if changed || e.updatedAt.IsZero() {
					e.updatedAt = now
					e.expiresAt = expires
					p.opts.Log.Info("projected: wrote a credential of a DenoPod", "pod", ref.Key(), "name", c.Name, "path", path)
				}
			}
		}
		env := v1alpha1.ProjectedEnvName(c.Name)
		out.env[env] = path
		out.reads = append(out.reads, path)
		st := v1alpha1.DenoPodProjected{Name: c.Name, Env: env, Path: path, Message: e.message}
		if !e.updatedAt.IsZero() {
			st.UpdatedAt = e.updatedAt.UTC().Format(time.RFC3339)
		}
		if !e.expiresAt.IsZero() {
			st.ExpiresAt = e.expiresAt.UTC().Format(time.RFC3339)
		}
		out.status = append(out.status, st)
	}
	for name := range have {
		if !keep[name] {
			delete(have, name)
			_ = os.RemoveAll(filepath.Join(dir, name))
		}
	}
	return out, firstErr
}

func (p *Provider) projectedDue(c v1alpha1.ProjectedCredential, e *projectedEntry, now time.Time) bool {
	if e.message != "" {
		return now.Sub(e.checkedAt) >= ProjectedRetryInterval
	}
	if e.updatedAt.IsZero() {
		return true
	}
	if c.ServiceAccountToken != nil {
		ttl := e.expiresAt.Sub(e.updatedAt)
		return now.Sub(e.updatedAt) >= ttl/projectedTokenRefreshFraction
	}
	return now.Sub(e.checkedAt) >= ProjectedSourceCheckInterval
}

func (p *Provider) writeProjected(ctx context.Context, ref Ref, c v1alpha1.ProjectedCredential, path string) (bool, time.Time, error) {
	switch {
	case c.ServiceAccountToken != nil:
		return p.writeProjectedToken(ctx, ref, c.ServiceAccountToken, path)
	case c.KcpCA != nil:
		if len(p.clusterCA) == 0 {
			return false, time.Time{}, errors.New("the provider knows no kcp CA")
		}
		changed, err := writeProjectedFile(path, p.clusterCA, 0o644)
		return changed, time.Time{}, err
	case c.OpenBaoClient != nil:
		return p.writeProjectedOpenBao(ctx, ref, c.OpenBaoClient, path)
	case c.File != nil:
		body, err := os.ReadFile(c.File.Path)
		if err != nil {
			return false, time.Time{}, err
		}
		if err := onlyCertificates(body); err != nil {
			return false, time.Time{}, fmt.Errorf("%s: %w", c.File.Path, err)
		}
		changed, err := writeProjectedFile(path, body, 0o644)
		return changed, time.Time{}, err
	}
	return false, time.Time{}, errors.New("no source")
}

func (p *Provider) projectedWorkspace(ctx context.Context, ref Ref, want string) (string, error) {
	if want == "" {
		return ref.LogicalCluster, nil
	}
	own := ref.LogicalCluster
	if p.paths != nil {
		if path := p.paths.Lookup(ctx, ref.LogicalCluster); path != "" {
			own = path
		}
	}
	if want == own || strings.HasPrefix(own, want+":") {
		return want, nil
	}
	return "", fmt.Errorf("workspace %s is neither the pod's own workspace (%s) nor one of its parents", want, own)
}

func (p *Provider) writeProjectedToken(ctx context.Context, ref Ref, t *v1alpha1.ProjectedServiceAccountToken, path string) (bool, time.Time, error) {
	if p.opts.Minter == nil {
		return false, time.Time{}, errors.New("the provider has no token minter")
	}
	ws, err := p.projectedWorkspace(ctx, ref, t.Workspace)
	if err != nil {
		return false, time.Time{}, err
	}
	secs := t.ExpirationSeconds
	if secs == 0 {
		secs = DefaultProjectedTokenSeconds
	}
	ttl := time.Duration(secs) * time.Second
	token, err := p.opts.Minter.MintServiceAccountToken(ctx, ws, t.Namespace, t.Name, ttl)
	if err != nil {
		return false, time.Time{}, err
	}
	if _, err := writeProjectedFile(path, []byte(token), 0o600); err != nil {
		return false, time.Time{}, err
	}
	return true, p.opts.Now().Add(ttl), nil
}

func (p *Provider) writeProjectedOpenBao(ctx context.Context, ref Ref, o *v1alpha1.ProjectedOpenBaoClient, dir string) (bool, time.Time, error) {
	if p.opts.ProjectedSources == nil {
		return false, time.Time{}, errors.New("the provider has no OpenBaoServer source")
	}
	ws, err := p.projectedWorkspace(ctx, ref, o.Workspace)
	if err != nil {
		return false, time.Time{}, err
	}
	files, err := p.opts.ProjectedSources.OpenBaoClient(ctx, ws, o.Server, o.Client)
	if err != nil {
		return false, time.Time{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, time.Time{}, err
	}
	changed := false
	for _, f := range []struct {
		from, to string
		mode     os.FileMode
	}{{files.CA, ProjectedOpenBaoCAFile, 0o644}, {files.Cert, ProjectedOpenBaoCertFile, 0o644}, {files.Key, ProjectedOpenBaoKeyFile, 0o600}} {
		body, err := os.ReadFile(f.from)
		if err != nil {
			return false, time.Time{}, err
		}
		c, err := writeProjectedFile(filepath.Join(dir, f.to), body, f.mode)
		if err != nil {
			return false, time.Time{}, err
		}
		changed = changed || c
	}
	return changed, time.Time{}, nil
}

func onlyCertificates(body []byte) error {
	rest := bytes.TrimSpace(body)
	n := 0
	for len(rest) > 0 {
		block, tail := pem.Decode(rest)
		if block == nil {
			return errors.New("not a PEM certificate bundle")
		}
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("holds a %s block; a projected file is public certificates only", block.Type)
		}
		n++
		rest = bytes.TrimSpace(tail)
	}
	if n == 0 {
		return errors.New("holds no certificate")
	}
	return nil
}

func writeProjectedFile(path string, body []byte, mode os.FileMode) (bool, error) {
	if have, err := os.ReadFile(path); err == nil && bytes.Equal(have, body) {
		return false, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return false, err
	}
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return false, err
	}
	if err := tmp.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(tmp.Name(), path)
}

func (p *Provider) removeProjected(ref Ref) {
	p.projected.forget(ref.Key())
	if err := os.RemoveAll(p.projectedDir(ref)); err != nil {
		p.opts.Log.Warn("projected: the credentials of a deleted DenoPod were not removed", "pod", ref.Key(), "err", err.Error())
	}
}

func withProjected(perms *denospec.Permissions, pr projection) *denospec.Permissions {
	if len(pr.env) == 0 {
		return perms
	}
	out := &denospec.Permissions{}
	if perms != nil {
		c := *perms
		out = &c
	}
	out.Read = extendPermission(out.Read, pr.reads)
	out.Env = extendPermission(out.Env, []string{v1alpha1.ProjectedEnv})
	return out
}

func extendPermission(have *denospec.Permission, add []string) *denospec.Permission {
	if have != nil && have.Allow {
		return have
	}
	out := &denospec.Permission{}
	if have != nil {
		c := *have
		out = &c
	}
	list := slices.Clone(out.AllowList)
	for _, a := range add {
		if !slices.Contains(list, a) {
			list = append(list, a)
		}
	}
	out.AllowList = list
	return out
}

func projectedEnvValue(pr projection) (string, error) {
	b, err := json.Marshal(pr.env)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
