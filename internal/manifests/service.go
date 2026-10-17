package manifests

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"

	denov1alpha1 "github.com/johnandersen777/socialweb-computer-kcp/internal/denokcp/api/v1alpha1"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/kcplibs/factory/servicenames"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tsbundle"
)

const (
	ServiceTemplatePath = "denopods/service.yaml"
	EntriesPath         = "ts-entries.yaml"
	TLSServe            = "serve"
	TLSTrust            = "trust"
	TLSNone             = "none"
	TSRootRef           = "${SOCIALWEB_TS_ROOT}"
)

var (
	ErrAllowAll      = errors.New("manifests: the service template refuses -A/--allow-all")
	ErrUnreasoned    = errors.New("manifests: --allow-run and --allow-ffi render only with a reason")
	serviceName      = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	templateOwnedEnv = []string{"HOME", "TMPDIR", "DENO_DIR", "DENO_CERT", "PATH", "PORT_FILE", "LISTENERS_FILE", "STATUS_FILE"}
)

func UnderTSRoot(rel string) string { return TSRootRef + "/" + strings.TrimPrefix(rel, "/") }

type Service struct {
	Name           string                             `json:"name"`
	Entry          string                             `json:"entry"`
	StateDir       string                             `json:"stateDir"`
	ServiceAccount string                             `json:"serviceAccount"`
	Memory         string                             `json:"memory"`
	TLS            string                             `json:"tls"`
	Trust          []string                           `json:"trust,omitempty"`
	Args           []string                           `json:"args"`
	Env            map[string]string                  `json:"env,omitempty"`
	Permissions    *ServicePermissions                `json:"permissions"`
	Probe          ServiceProbe                       `json:"probe"`
	Projected      []denov1alpha1.ProjectedCredential `json:"projected,omitempty"`
	Edge           string                             `json:"edge,omitempty"`
}

type ServicePermissions struct {
	All   bool           `json:"all,omitempty"`
	Net   bool           `json:"net,omitempty"`
	Env   NameGrant      `json:"env,omitzero"`
	Sys   NameGrant      `json:"sys,omitzero"`
	Read  []string       `json:"read,omitempty"`
	Write []string       `json:"write,omitempty"`
	Run   *ReasonedGrant `json:"run,omitempty"`
	FFI   *ReasonedGrant `json:"ffi,omitempty"`
}

type ReasonedGrant struct {
	Allow  []string `json:"allow"`
	Reason string   `json:"reason"`
}

type ServiceProbe struct {
	Host string `json:"host"`
	Path string `json:"path"`
}

type servicePermissionsWire struct {
	Net   bool     `json:"net,omitempty"`
	Env   any      `json:"env,omitempty"`
	Sys   any      `json:"sys,omitempty"`
	Read  []string `json:"read,omitempty"`
	Write []string `json:"write,omitempty"`
	Run   []string `json:"run,omitempty"`
	FFI   []string `json:"ffi,omitempty"`
}

type entriesFile struct {
	Entries []tsbundle.EntrySpec `json:"entries"`
}

func LoadEntries() (map[string]tsbundle.EntrySpec, error) {
	b, err := readFile(EntriesPath)
	if err != nil {
		return nil, err
	}
	var f entriesFile
	if err := yaml.UnmarshalStrict(b, &f); err != nil {
		return nil, fmt.Errorf("manifests: %s: %w", EntriesPath, err)
	}
	if err := tsbundle.CheckEntries(f.Entries); err != nil {
		return nil, fmt.Errorf("manifests: %s: %w", EntriesPath, err)
	}
	out := map[string]tsbundle.EntrySpec{}
	for _, e := range f.Entries {
		out[e.Name] = e
	}
	return out, nil
}

func DecodeService(where string, body []byte) (Service, error) {
	var s Service
	if err := yaml.UnmarshalStrict(body, &s); err != nil {
		return Service{}, fmt.Errorf("manifests: %s: %w", where, err)
	}
	return s, nil
}

func (s Service) Check(entries map[string]tsbundle.EntrySpec) error {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	if !serviceName.MatchString(s.Name) {
		add("name %q is not a DNS label", s.Name)
	}
	if _, ok := entries[s.Entry]; !ok {
		add("entry %q is no entry of deploy/%s; a service runs only what the closure ships", s.Entry, EntriesPath)
	}
	if !path.IsAbs(s.StateDir) {
		add("stateDir %q is not an absolute path", s.StateDir)
	}
	if s.ServiceAccount != "" && !serviceName.MatchString(s.ServiceAccount) {
		add("serviceAccount %q is not a DNS label; a service that names a ServiceAccount gets kcp tokens minted for it", s.ServiceAccount)
	}
	switch s.TLS {
	case TLSServe, TLSTrust, TLSNone:
	default:
		add("tls %q is not one of %s, %s, %s", s.TLS, TLSServe, TLSTrust, TLSNone)
	}
	for _, t := range s.Trust {
		if !path.IsAbs(t) || strings.Contains(t, ",") {
			add("trust names absolute PEM files without commas, got %q", t)
		}
	}
	for k := range s.Env {
		if slices.Contains(templateOwnedEnv, k) {
			add("env %s is set by the template and never by a service", k)
		}
	}
	if _, err := resource.ParseQuantity(s.Memory); err != nil || s.Memory == "" {
		add("memory %q is not a quantity such as 512Mi; every service has a memory limit", s.Memory)
	}
	if s.Probe.Host == "" || !strings.HasPrefix(s.Probe.Path, "/") {
		add("probe needs a host and a path starting with /")
	}
	if err := s.Permissions.check(); err != nil {
		add("permissions: %v", err)
	}
	if err := denov1alpha1.CheckProjected(s.Projected); err != nil {
		add("%v", err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("manifests: service %s: %s", s.Name, strings.Join(problems, "; "))
	}
	return nil
}

func (p *ServicePermissions) check() error {
	if p == nil {
		return errors.New("a service names its permissions; there is no default")
	}
	if p.All {
		return ErrAllowAll
	}
	for _, list := range [][]string{p.Read, p.Write} {
		for _, d := range list {
			if !path.IsAbs(d) || strings.Contains(d, ",") {
				return fmt.Errorf("read and write name absolute paths without commas, got %q", d)
			}
		}
	}
	for name, g := range map[string]NameGrant{"env": p.Env, "sys": p.Sys} {
		if err := g.check(); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	for name, g := range map[string]*ReasonedGrant{"run": p.Run, "ffi": p.FFI} {
		if g == nil {
			continue
		}
		if strings.TrimSpace(g.Reason) == "" {
			return fmt.Errorf("%w: %s names no reason", ErrUnreasoned, name)
		}
		if len(g.Allow) == 0 || slices.ContainsFunc(g.Allow, func(a string) bool { return a == "" || strings.Contains(a, ",") }) {
			return fmt.Errorf("%s names no program, or an empty one", name)
		}
	}
	return nil
}

func (p *ServicePermissions) wire() servicePermissionsWire {
	w := servicePermissionsWire{Net: p.Net, Env: p.Env.wire(), Sys: p.Sys.wire(), Read: p.Read, Write: p.Write}
	if p.Run != nil {
		w.Run = p.Run.Allow
	}
	if p.FFI != nil {
		w.FFI = p.FFI.Allow
	}
	return w
}

func jsonText(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("manifests: %w", err)
	}
	return string(b), nil
}

func quoted(v any) (string, error) {
	inner, err := jsonText(v)
	if err != nil {
		return "", err
	}
	return jsonText(inner)
}

func (s Service) TemplateData(entries map[string]tsbundle.EntrySpec) (map[string]any, error) {
	if err := s.Check(entries); err != nil {
		return nil, err
	}
	e := entries[s.Entry]
	flags := make([]string, 0, len(e.Unstable))
	for _, u := range e.Unstable {
		flags = append(flags, "--unstable-"+u)
	}
	args := s.Args
	if args == nil {
		args = []string{}
	}
	env := s.Env
	if env == nil {
		env = map[string]string{}
	}
	preloads := e.Preload
	if preloads == nil {
		preloads = []string{}
	}
	argsText, err := jsonText(args)
	if err != nil {
		return nil, err
	}
	envText, err := jsonText(env)
	if err != nil {
		return nil, err
	}
	if fixed := servicenames.FixedPorts(map[string]string{servicenames.ArgsKey: argsText, servicenames.EnvKey: envText}); len(fixed) > 0 && s.Edge == "" {
		return nil, fmt.Errorf("manifests: service %s binds a fixed port (%s); every service behind the edge binds port 0 and reports its listener",
			s.Name, strings.Join(fixed, ", "))
	}
	tls := "false"
	if s.TLS != TLSNone {
		tls = "true"
	}
	data := map[string]any{"Name": s.Name, "ServiceAccount": "", "Annotations": ""}
	if s.Edge != "" {
		text, err := jsonText(map[string]string{denov1alpha1.AnnotationEdge: s.Edge})
		if err != nil {
			return nil, err
		}
		data["Annotations"] = "\n  annotations: " + text
	}
	if s.ServiceAccount != "" {
		text, err := jsonText(map[string]string{"name": s.ServiceAccount, "namespace": "default"})
		if err != nil {
			return nil, err
		}
		data["ServiceAccount"] = "\n  serviceAccount: " + text
	}
	data["Resources"] = "\n  resources:\n    memory: " + s.Memory
	data["Projected"] = ""
	if len(s.Projected) > 0 {
		text, err := jsonText(s.Projected)
		if err != nil {
			return nil, err
		}
		data["Projected"] = "\n  projected: " + text
	}
	for key, v := range map[string]any{"Probe": []string{"kcpdns", s.Probe.Host, s.Probe.Path}} {
		text, err := jsonText(v)
		if err != nil {
			return nil, err
		}
		data[key] = text
	}
	for key, v := range map[string]any{"Entry": e.Path, "StateDir": s.StateDir, "TLS": tls, "TLSMode": s.TLS} {
		text, err := jsonText(v)
		if err != nil {
			return nil, err
		}
		data[key] = text
	}
	trust := s.Trust
	if trust == nil {
		trust = []string{}
	}
	for key, v := range map[string]any{"Args": args, "Env": env, "Preloads": preloads, "DenoFlags": flags, "Permissions": s.Permissions.wire(), "Trust": trust} {
		text, err := quoted(v)
		if err != nil {
			return nil, err
		}
		data[key] = text
	}
	return data, nil
}

func renderService(valuesPath, templatePath string, values []byte, entries map[string]tsbundle.EntrySpec) ([]byte, error) {
	s, err := DecodeService(valuesPath, values)
	if err != nil {
		return nil, err
	}
	data, err := s.TemplateData(entries)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", valuesPath, err)
	}
	src, err := readFile(templatePath)
	if err != nil {
		return nil, err
	}
	return execute(templatePath, string(src), data)
}
