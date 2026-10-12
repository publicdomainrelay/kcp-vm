package hostprocess

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"

	"github.com/johnandersen777/socialweb-computer-kcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/hostsvc"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpapply"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcpserver"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/manifests"
)

const (
	ManifestSet      = "hostprocesses"
	ProviderSet      = "provider"
	DefaultWorkspace = "root"
	DefaultInterval  = 5 * time.Second
	RetryAfter       = 2 * time.Second
	MirrorPrefix     = "bootstrap-"
	MirrorLabel      = "socialweb.computer/placement"
	ConditionReady   = "Ready"
	ConditionLimits  = "LimitsEnforced"
)

var GVR = schema.GroupVersionResource{Group: v1alpha1.GroupName, Version: v1alpha1.Version, Resource: "hostprocesses"}

type Mirror struct {
	Role  string
	Unit  string
	Exec  []string
	Slice string
	After []string
	Wants []string
	Port  int
}

type Config struct {
	Kubeconfig   string
	Workspace    string
	Manager      hostsvc.Manager
	Prefix       string
	Resolve      func(v1alpha1.HostProcessAsset) (string, error)
	Mirrors      func() []Mirror
	Interval     time.Duration
	ApplyTimeout time.Duration
	Log          *slog.Logger
}

type Supervisor struct {
	c       Config
	client  dynamic.ResourceInterface
	placed  map[string]bool
	listed  bool
	backend hostsvc.Backend
}

func (c Config) log() *slog.Logger {
	if c.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return c.Log.With("component", "hostprocess")
}

func (c Config) workspace() string {
	if c.Workspace == "" {
		return DefaultWorkspace
	}
	return c.Workspace
}

func applySets(ctx context.Context, c Config, restCfg *rest.Config) error {
	ap, err := kcpapply.New(restCfg, kcpapply.Options{WaitTimeout: c.ApplyTimeout, Log: c.log()})
	if err != nil {
		return err
	}
	ix, err := manifests.Load()
	if err != nil {
		return err
	}
	for _, name := range []string{ProviderSet, ManifestSet} {
		set, ok := ix.Set(name)
		if !ok {
			return fmt.Errorf("hostprocess: deploy/sets.yaml names no %s set", name)
		}
		values, err := manifests.DecodeValues(set.Values, nil)
		if err != nil {
			return err
		}
		if _, err := ap.Apply(ctx, set, values); err != nil {
			return fmt.Errorf("hostprocess: applying the %s set: %w", name, err)
		}
	}
	return nil
}

func New(ctx context.Context, c Config) (*Supervisor, error) {
	restCfg, err := kcpserver.FrontRestConfig(c.Kubeconfig)
	if err != nil {
		return nil, err
	}
	if err := applySets(ctx, c, restCfg); err != nil {
		return nil, err
	}
	cfg := rest.CopyConfig(restCfg)
	cfg.Host = strings.TrimSuffix(cfg.Host, "/") + "/clusters/" + c.workspace()
	dc, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Supervisor{c: c, client: dc.Resource(GVR), placed: map[string]bool{}, backend: c.Manager.Capabilities().Backend}, nil
}

func toObject(u *unstructured.Unstructured) (v1alpha1.HostProcess, error) {
	var o v1alpha1.HostProcess
	err := runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &o)
	return o, err
}

func toUnstructured(o *v1alpha1.HostProcess) (*unstructured.Unstructured, error) {
	o.APIVersion = v1alpha1.GroupVersion.String()
	o.Kind = "HostProcess"
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(o)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: m}, nil
}

func MirrorName(role string) string { return MirrorPrefix + role }

func (s *Supervisor) syncMirrors(ctx context.Context) {
	if s.c.Mirrors == nil {
		return
	}
	for _, m := range s.c.Mirrors() {
		name := MirrorName(m.Role)
		want := v1alpha1.HostProcessSpec{Unit: m.Unit, Placement: v1alpha1.HostProcessPlacementBootstrap, Slice: m.Slice,
			After: m.After, Wants: m.Wants}
		if len(m.Exec) > 0 {
			want.Exec, want.Argv = m.Exec[0], m.Exec[1:]
		}
		cur, err := s.client.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			o := v1alpha1.HostProcess{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{MirrorLabel: string(v1alpha1.HostProcessPlacementBootstrap)}}, Spec: want}
			u, err := toUnstructured(&o)
			if err == nil {
				_, err = s.client.Create(ctx, u, metav1.CreateOptions{})
			}
			if err != nil {
				s.c.log().Warn("writing a bootstrap mirror", "name", name, "err", err.Error())
			}
			continue
		}
		if err != nil {
			s.c.log().Warn("reading a bootstrap mirror", "name", name, "err", err.Error())
			continue
		}
		o, err := toObject(cur)
		if err != nil {
			continue
		}
		if o.Spec.Unit != want.Unit || o.Spec.Exec != want.Exec || !slices.Equal(o.Spec.Argv, want.Argv) || o.Spec.Placement != want.Placement {
			o.Spec = want
			if u, err := toUnstructured(&o); err == nil {
				if _, err := s.client.Update(ctx, u, metav1.UpdateOptions{}); err != nil {
					s.c.log().Warn("updating a bootstrap mirror", "name", name, "err", err.Error())
				}
			}
		}
	}
}

func (s *Supervisor) observe(ctx context.Context, o v1alpha1.HostProcess) Observed {
	m := s.c.Manager
	obs := Observed{Object: o, Backend: s.backend, Prefix: s.c.Prefix, Deleting: o.DeletionTimestamp != nil}
	if o.Spec.Placement == v1alpha1.HostProcessPlacementBootstrap {
		obs.Prefix = ""
	}
	obs.Unit, obs.Inspect = m.Inspect(ctx, o.Spec.Unit)
	if obs.Inspect != nil {
		return obs
	}
	if o.Spec.Asset != nil && s.c.Resolve != nil {
		if p, err := s.c.Resolve(*o.Spec.Asset); err == nil {
			obs.Exec = p
		}
	}
	rt := o.Status.Runtime
	if rt != nil && rt.InvocationID != "" && rt.Backend == string(s.backend) && o.Spec.Placement != v1alpha1.HostProcessPlacementBootstrap &&
		o.Status.Phase != v1alpha1.HostProcessPhaseLost {
		want := hostsvc.Identity{Unit: o.Spec.Unit, InvocationID: rt.InvocationID, PID: int(rt.Pid), StartTicks: uint64(rt.StartTicks),
			BootID: rt.HostBootID, Backend: hostsvc.Backend(rt.Backend), Manager: rt.Manager}
		c, err := m.Check(ctx, want)
		if err != nil {
			obs.Inspect = err
			return obs
		}
		obs.Check = &c
	}
	return obs
}

func setCondition(o *v1alpha1.HostProcess, t string, ok bool, reason, msg string) {
	st := metav1.ConditionFalse
	if ok {
		st = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&o.Status.Conditions, metav1.Condition{Type: t, Status: st, Reason: reason, Message: msg,
		ObservedGeneration: o.Generation})
}

func (s *Supervisor) apply(ctx context.Context, o v1alpha1.HostProcess, r Result) v1alpha1.HostProcess {
	m := s.c.Manager
	log := s.c.log().With("hostprocess", o.Name, "unit", o.Spec.Unit)
	for _, op := range r.Ops {
		switch op.Kind {
		case OpStart:
			st, err := m.Start(ctx, op.Spec)
			if err != nil {
				r.Phase, r.Message = v1alpha1.HostProcessPhaseFailed, err.Error()
				log.Warn("start", "err", err.Error())
				continue
			}
			s.placed[op.Unit] = true
			log.Info("placed", "state", string(st.State), "adopted", st.Adopted)
		case OpStop:
			if err := m.Stop(ctx, op.Unit); err != nil {
				log.Warn("stop", "err", err.Error())
				continue
			}
			delete(s.placed, op.Unit)
			log.Info("stopped")
		}
	}
	next := o
	if r.Unknown {
		next.Status.Message = r.Message
		return next
	}
	next.Status.Phase, next.Status.LostReason, next.Status.Message = r.Phase, r.LostReason, r.Message
	next.Status.ObservedGeneration = o.Generation
	if r.Identify {
		if id, err := m.Identify(ctx, o.Spec.Unit, 0); err == nil {
			st, _ := m.Inspect(ctx, o.Spec.Unit)
			next.Status.Runtime = &v1alpha1.HostProcessRuntime{Backend: string(id.Backend), Unit: id.Unit, InvocationID: id.InvocationID,
				Pid: int64(id.PID), StartTicks: int64(id.StartTicks), HostBootID: id.BootID, Manager: id.Manager}
			if !st.ActiveSince.IsZero() {
				next.Status.Runtime.ActiveSince = st.ActiveSince.UTC().Format(time.RFC3339Nano)
			}
			if o.Spec.Placement == v1alpha1.HostProcessPlacementSupervisor {
				s.placed[o.Spec.Unit] = true
			}
		}
	}
	if next.Status.Runtime != nil && next.Status.Runtime.Pid > 0 && next.Status.Phase == v1alpha1.HostProcessPhaseRunning {
		if port, err := hostsvc.ListenPort(int(next.Status.Runtime.Pid)); err == nil {
			next.Status.Listeners = []v1alpha1.HostProcessListener{{Name: "tcp", Address: "127.0.0.1:" + strconv.Itoa(port)}}
		} else {
			next.Status.Listeners = nil
		}
	}
	if r.Phase == v1alpha1.HostProcessPhaseStopped || r.Phase == v1alpha1.HostProcessPhaseFailed || r.Phase == v1alpha1.HostProcessPhaseLost {
		if st, err := m.Inspect(ctx, o.Spec.Unit); err == nil && st.State != hostsvc.NotFound {
			next.Status.LastResult = &v1alpha1.HostProcessResult{Result: st.Result, ExitCode: st.ExecMainStatus,
				At: time.Now().UTC().Format(time.RFC3339)}
		}
	}
	setCondition(&next, ConditionReady, next.Status.Phase == v1alpha1.HostProcessPhaseRunning, string(next.Status.Phase), next.Status.Message)
	caps := m.Capabilities()
	setCondition(&next, ConditionLimits, caps.EnforcesLimits, string(caps.Backend), "limits are enforced only where the backend has cgroups")
	return next
}

func (s *Supervisor) writeStatus(ctx context.Context, cur *unstructured.Unstructured, next v1alpha1.HostProcess) error {
	u, err := toUnstructured(&next)
	if err != nil {
		return err
	}
	if equalStatus(cur.Object["status"], u.Object["status"]) {
		return nil
	}
	u.SetResourceVersion(cur.GetResourceVersion())
	_, err = s.client.UpdateStatus(ctx, u, metav1.UpdateOptions{})
	return err
}

func equalStatus(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

func (s *Supervisor) Pass(ctx context.Context) error {
	s.syncMirrors(ctx)
	list, err := s.client.List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for i := range list.Items {
		cur := &list.Items[i]
		o, err := toObject(cur)
		if err != nil {
			s.c.log().Warn("decoding", "name", cur.GetName(), "err", err.Error())
			continue
		}
		seen[o.Spec.Unit] = true
		next := s.apply(ctx, o, Reconcile(s.observe(ctx, o)))
		if err := s.writeStatus(ctx, cur, next); err != nil && !apierrors.IsConflict(err) {
			s.c.log().Warn("writing status", "name", o.Name, "err", err.Error())
		}
	}
	if s.c.Prefix != "" {
		units, err := s.c.Manager.List(ctx, s.c.Prefix)
		if err == nil {
			for _, u := range units {
				if !s.placed[u.Name] && s.listed {
					continue
				}
				if !seen[u.Name] && (u.State == hostsvc.Active || u.State == hostsvc.Activating) {
					s.c.log().Info("the HostProcess of a placed unit is gone; stopping the unit", "unit", u.Name)
					if err := s.c.Manager.Stop(ctx, u.Name); err == nil {
						delete(s.placed, u.Name)
					}
				}
			}
		}
	}
	s.listed = true
	return nil
}

func (s *Supervisor) Run(ctx context.Context) error {
	interval := s.c.Interval
	if interval == 0 {
		interval = DefaultInterval
	}
	s.c.log().Info("the HostProcess supervisor runs in the core", "workspace", s.c.workspace(), "backend", string(s.backend),
		"prefix", s.c.Prefix)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := s.Pass(ctx); err != nil {
			s.c.log().Warn("a reconcile pass failed; units keep running", "err", err.Error())
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

func Supervise(ctx context.Context, c Config) {
	log := c.log()
	for {
		s, err := New(ctx, c)
		if err == nil {
			err = s.Run(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		log.Error("the HostProcess supervisor stopped; starting it again", "err", fmt.Sprint(err), "after", RetryAfter.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(RetryAfter):
		}
	}
}
