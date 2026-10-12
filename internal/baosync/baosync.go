package baosync

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

const (
	MountKV2     = "kv-v2"
	MountKV      = "kv"
	MountPKI     = "pki"
	AuthCert     = "cert"
	AuthJWT      = "jwt"
	AuthAppRole  = "approle"
	AuthToken    = "token"
	policyRoot   = "root"
	policyDflt   = "default"
	mountSys     = "sys"
	mountIdent   = "identity"
	mountCubby   = "cubbyhole"
	collCerts    = "certs"
	collRole     = "role"
	collPKIRoles = "roles"
)

type SecretMount struct {
	Path        string
	Type        string
	MaxLeaseTTL string
}

type AuthMount struct {
	Path string
	Type string
}

type Policy struct {
	Name string
	HCL  string
}

type Object struct {
	Path string
	Data map[string]any
}

type KVScope struct {
	Mount  string
	Prefix string
	Keys   []string
}

type Desired struct {
	SecretMounts []SecretMount
	AuthMounts   []AuthMount
	Policies     []Policy
	Objects      []Object
	KV           []KVScope
}

type Actual struct {
	SecretMounts map[string]string
	AuthMounts   map[string]string
	Policies     map[string]string
	Collections  map[string][]string
	Objects      map[string]map[string]any
	KV           map[string][]string
}

type OpKind string

const (
	OpEnableSecret  OpKind = "enable-secret-mount"
	OpEnableAuth    OpKind = "enable-auth-mount"
	OpDisableAuth   OpKind = "disable-auth-mount"
	OpPutPolicy     OpKind = "put-policy"
	OpWriteObject   OpKind = "write"
	OpDeleteObject  OpKind = "delete"
	OpDeletePolicy  OpKind = "delete-policy"
	OpDisableSecret OpKind = "disable-secret-mount"
	OpDeleteKV      OpKind = "delete-kv"
)

type Op struct {
	Kind   OpKind
	Target string
	Type   string
	Body   map[string]any
	HCL    string
	Why    string
}

func (o Op) String() string { return string(o.Kind) + " " + o.Target }

type Plan struct {
	Ops       []Op
	Conflicts []string
}

func BuiltinSecretMount(p string) bool {
	switch strings.Trim(p, "/") {
	case mountSys, mountIdent, mountCubby:
		return true
	}
	return false
}

func BuiltinAuthMount(p string) bool { return strings.Trim(p, "/") == AuthToken }

func BuiltinPolicy(name string) bool { return name == policyRoot || name == policyDflt }

func RoleCollection(mountPath, mountType string) string {
	m := strings.Trim(mountPath, "/")
	switch mountType {
	case AuthCert:
		return "auth/" + m + "/" + collCerts
	case AuthJWT, AuthAppRole:
		return "auth/" + m + "/" + collRole
	}
	return ""
}

func PKIRoleCollection(mountPath string) string {
	return strings.Trim(mountPath, "/") + "/" + collPKIRoles
}

func KVScopeKey(s KVScope) string {
	return strings.Trim(s.Mount, "/") + "/metadata/" + strings.Trim(s.Prefix, "/")
}

func (d Desired) Collections() []string {
	var out []string
	for _, m := range d.AuthMounts {
		if c := RoleCollection(m.Path, m.Type); c != "" {
			out = append(out, c)
		}
	}
	for _, m := range d.SecretMounts {
		if m.Type == MountPKI {
			out = append(out, PKIRoleCollection(m.Path))
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

func (d Desired) Validate() error {
	secrets, auths, policies, objects := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, m := range d.SecretMounts {
		p := strings.Trim(m.Path, "/")
		if p == "" || BuiltinSecretMount(p) || secrets[p] {
			return fmt.Errorf("baosync: secret mount %q is empty, a built-in or declared twice", m.Path)
		}
		switch m.Type {
		case MountKV2, MountKV, MountPKI:
		default:
			return fmt.Errorf("baosync: secret mount %s has type %q; kv-v2, kv and pki are declared", p, m.Type)
		}
		secrets[p] = true
	}
	for _, m := range d.AuthMounts {
		p := strings.Trim(m.Path, "/")
		if p == "" || BuiltinAuthMount(p) || auths[p] {
			return fmt.Errorf("baosync: auth mount %q is empty, a built-in or declared twice", m.Path)
		}
		switch m.Type {
		case AuthCert, AuthJWT, AuthAppRole:
		default:
			return fmt.Errorf("baosync: auth mount %s has type %q; cert, jwt and approle are declared", p, m.Type)
		}
		auths[p] = true
	}
	for _, p := range d.Policies {
		if p.Name == "" || BuiltinPolicy(p.Name) || policies[p.Name] || strings.ContainsAny(p.Name, "/ ") {
			return fmt.Errorf("baosync: policy %q is empty, a built-in, holds a slash or a space, or is declared twice", p.Name)
		}
		policies[p.Name] = true
	}
	for _, o := range d.Objects {
		p := strings.Trim(o.Path, "/")
		if p == "" || objects[p] {
			return fmt.Errorf("baosync: object %q is empty or declared twice", o.Path)
		}
		objects[p] = true
	}
	for _, s := range d.KV {
		if !secrets[strings.Trim(s.Mount, "/")] {
			return fmt.Errorf("baosync: kv scope %s names no declared secret mount", KVScopeKey(s))
		}
	}
	return nil
}

func Diff(d Desired, a Actual) Plan {
	var plan Plan
	add := func(o Op) { plan.Ops = append(plan.Ops, o) }

	wantSecret := map[string]SecretMount{}
	for _, m := range d.SecretMounts {
		p := strings.Trim(m.Path, "/")
		wantSecret[p] = m
		got, ok := a.SecretMounts[p]
		switch {
		case !ok:
			add(Op{Kind: OpEnableSecret, Target: p, Type: m.Type, Body: maxLease(m.MaxLeaseTTL), Why: "declared and missing"})
		case got != m.Type:
			plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("secret mount %s is %s and is declared %s; a secret mount is never "+
				"replaced, because disabling it destroys what it holds", p, got, m.Type))
		}
	}
	wantAuth := map[string]AuthMount{}
	for _, m := range d.AuthMounts {
		p := strings.Trim(m.Path, "/")
		wantAuth[p] = m
		got, ok := a.AuthMounts[p]
		switch {
		case !ok:
			add(Op{Kind: OpEnableAuth, Target: p, Type: m.Type, Why: "declared and missing"})
		case got != m.Type:
			add(Op{Kind: OpDisableAuth, Target: p, Why: "type " + got + " where " + m.Type + " is declared"})
			add(Op{Kind: OpEnableAuth, Target: p, Type: m.Type, Why: "declared as " + m.Type})
		}
	}
	wantPolicy := map[string]bool{}
	for _, p := range d.Policies {
		wantPolicy[p.Name] = true
		got, ok := a.Policies[p.Name]
		switch {
		case !ok:
			add(Op{Kind: OpPutPolicy, Target: p.Name, HCL: p.HCL, Why: "declared and missing"})
		case strings.TrimSpace(got) != strings.TrimSpace(p.HCL):
			add(Op{Kind: OpPutPolicy, Target: p.Name, HCL: p.HCL, Why: "its rules drifted from the declaration"})
		}
	}
	wantObject := map[string]bool{}
	for _, o := range d.Objects {
		p := strings.Trim(o.Path, "/")
		wantObject[p] = true
		got, ok := a.Objects[p]
		switch {
		case !ok:
			add(Op{Kind: OpWriteObject, Target: p, Body: o.Data, Why: "declared and missing"})
		default:
			if k := Drifted(o.Data, got); k != "" {
				add(Op{Kind: OpWriteObject, Target: p, Body: o.Data, Why: "field " + k + " drifted from the declaration"})
			}
		}
	}
	for _, c := range d.Collections() {
		for _, name := range a.Collections[c] {
			if strings.HasSuffix(name, "/") {
				continue
			}
			p := c + "/" + name
			if !wantObject[p] {
				add(Op{Kind: OpDeleteObject, Target: p, Why: "no declaration names it"})
			}
		}
	}
	for _, name := range sortedKeys(a.Policies) {
		if !wantPolicy[name] && !BuiltinPolicy(name) {
			add(Op{Kind: OpDeletePolicy, Target: name, Why: "no declaration names it"})
		}
	}
	for _, p := range sortedKeys(a.AuthMounts) {
		if _, ok := wantAuth[p]; !ok && !BuiltinAuthMount(p) {
			add(Op{Kind: OpDisableAuth, Target: p, Why: "no declaration names it"})
		}
	}
	for _, p := range sortedKeys(a.SecretMounts) {
		if _, ok := wantSecret[p]; !ok && !BuiltinSecretMount(p) {
			add(Op{Kind: OpDisableSecret, Target: p, Why: "no declaration names it"})
		}
	}
	for _, s := range d.KV {
		key := KVScopeKey(s)
		keep := map[string]bool{}
		for _, k := range s.Keys {
			keep[strings.Trim(k, "/")] = true
		}
		for _, name := range a.KV[key] {
			if name == "" || strings.HasSuffix(name, "/") || keep[name] {
				continue
			}
			add(Op{Kind: OpDeleteKV, Target: strings.TrimSuffix(key, "/") + "/" + name, Why: "no declaration names this entry"})
		}
	}
	return plan
}

func maxLease(ttl string) map[string]any {
	if ttl == "" {
		return nil
	}
	return map[string]any{"max_lease_ttl": ttl}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (d Desired) String() string {
	return fmt.Sprintf("%d secret mounts, %d auth mounts, %d policies, %d roles and configs, %d kv scopes",
		len(d.SecretMounts), len(d.AuthMounts), len(d.Policies), len(d.Objects), len(d.KV))
}
