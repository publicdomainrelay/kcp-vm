package baosync

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/openbao"
)

func mountType(m openbao.Mount) string {
	if m.Type == MountKV && fmt.Sprint(m.Options["version"]) == "2" {
		return MountKV2
	}
	return m.Type
}

func objectMounted(p string, a Actual) bool {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	if segs[0] == "auth" {
		if len(segs) < 2 {
			return false
		}
		_, ok := a.AuthMounts[segs[1]]
		return ok
	}
	_, ok := a.SecretMounts[segs[0]]
	return ok
}

func Read(ctx context.Context, c *openbao.Client, d Desired) (Actual, error) {
	a := Actual{SecretMounts: map[string]string{}, AuthMounts: map[string]string{}, Policies: map[string]string{},
		Collections: map[string][]string{}, Objects: map[string]map[string]any{}, KV: map[string][]string{}}
	secrets, err := c.SecretMounts(ctx)
	if err != nil {
		return a, fmt.Errorf("baosync: listing the secret mounts: %w; an unread list is not an empty one", err)
	}
	for p, m := range secrets {
		a.SecretMounts[p] = mountType(m)
	}
	auths, err := c.AuthMounts(ctx)
	if err != nil {
		return a, fmt.Errorf("baosync: listing the auth mounts: %w; an unread list is not an empty one", err)
	}
	for p, m := range auths {
		a.AuthMounts[p] = m.Type
	}
	names, err := c.List(ctx, "sys/policies/acl")
	if err != nil {
		return a, fmt.Errorf("baosync: listing the policies: %w; an unread list is not an empty one", err)
	}
	if len(names) == 0 {
		return a, errors.New("baosync: the policy list came back empty; a server always holds the default policy, so the read is wrong")
	}
	want := map[string]bool{}
	for _, p := range d.Policies {
		want[p.Name] = true
	}
	for _, n := range names {
		a.Policies[n] = ""
		if !want[n] {
			continue
		}
		hcl, err := c.GetPolicy(ctx, n)
		if err != nil {
			return a, fmt.Errorf("baosync: reading policy %s: %w", n, err)
		}
		a.Policies[n] = hcl
	}
	for _, coll := range d.Collections() {
		if !objectMounted(coll, a) || !collectionTypeMatches(coll, d, a) {
			continue
		}
		keys, err := c.List(ctx, coll)
		if err != nil {
			return a, fmt.Errorf("baosync: listing %s: %w; an unread list is not an empty one", coll, err)
		}
		a.Collections[coll] = keys
	}
	for _, o := range d.Objects {
		p := strings.Trim(o.Path, "/")
		if !objectMounted(p, a) {
			continue
		}
		data, err := c.Read(ctx, p)
		if err != nil {
			continue
		}
		a.Objects[p] = data
	}
	for _, s := range d.KV {
		if _, ok := a.SecretMounts[strings.Trim(s.Mount, "/")]; !ok {
			continue
		}
		key := KVScopeKey(s)
		keys, err := c.List(ctx, key)
		if err != nil {
			return a, fmt.Errorf("baosync: listing %s: %w; an unread list is not an empty one", key, err)
		}
		a.KV[key] = keys
	}
	return a, nil
}

func collectionTypeMatches(coll string, d Desired, a Actual) bool {
	for _, m := range d.AuthMounts {
		if RoleCollection(m.Path, m.Type) == coll {
			return a.AuthMounts[strings.Trim(m.Path, "/")] == m.Type
		}
	}
	for _, m := range d.SecretMounts {
		if m.Type == MountPKI && PKIRoleCollection(m.Path) == coll {
			return a.SecretMounts[strings.Trim(m.Path, "/")] == MountPKI
		}
	}
	return false
}
