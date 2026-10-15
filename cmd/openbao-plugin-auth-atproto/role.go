package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/openbao/openbao/sdk/v2/framework"
	"github.com/openbao/openbao/sdk/v2/helper/tokenutil"
	"github.com/openbao/openbao/sdk/v2/logical"
)

const rolePrefix = "role/"

type roleEntry struct {
	tokenutil.TokenParams

	BoundDIDs    []string `json:"bound_dids"`
	BoundHandles []string `json:"bound_handles"`
}

func (r *roleEntry) allows(did, handle string) error {
	for _, b := range r.BoundDIDs {
		if b == "*" || b == did {
			return nil
		}
	}
	if handle != "" {
		for _, b := range r.BoundHandles {
			if b == "*" || b == handle {
				return nil
			}
		}
	}
	if len(r.BoundDIDs) == 0 && handle == "" {
		return fmt.Errorf("the role admits handles only; sign in with a handle")
	}
	return fmt.Errorf("the role does not admit %s", did)
}

func getRole(ctx context.Context, s logical.Storage, name string) (*roleEntry, error) {
	raw, err := s.Get(ctx, rolePrefix+name)
	if err != nil || raw == nil {
		return nil, err
	}
	var r roleEntry
	if err := raw.DecodeJSON(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

func pathRoleList(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "role/?$",
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.ListOperation: &framework.PathOperation{Callback: b.roleList},
		},
		HelpSynopsis: "Lists the roles.",
	}
}

func pathRole(b *backend) *framework.Path {
	fields := map[string]*framework.FieldSchema{
		"name": {Type: framework.TypeString, Description: "Name of the role.", Required: true},
		"bound_dids": {
			Type:        framework.TypeCommaStringSlice,
			Description: "DIDs that may log in with this role; * admits every DID.",
		},
		"bound_handles": {
			Type:        framework.TypeCommaStringSlice,
			Description: "Handles that may log in with this role, by a handle login; * admits every handle.",
		},
	}
	tokenutil.AddTokenFields(fields)
	return &framework.Path{
		Pattern:        "role/" + framework.GenericNameRegex("name"),
		Fields:         fields,
		ExistenceCheck: b.roleExists,
		Operations: map[logical.Operation]framework.OperationHandler{
			logical.CreateOperation: &framework.PathOperation{Callback: b.roleWrite},
			logical.UpdateOperation: &framework.PathOperation{Callback: b.roleWrite},
			logical.ReadOperation:   &framework.PathOperation{Callback: b.roleRead},
			logical.DeleteOperation: &framework.PathOperation{Callback: b.roleDelete},
		},
		HelpSynopsis: "Manages a role: who may log in with it and the token parameters of the login.",
	}
}

func (b *backend) roleExists(ctx context.Context, req *logical.Request, d *framework.FieldData) (bool, error) {
	r, err := getRole(ctx, req.Storage, d.Get("name").(string))
	return r != nil, err
}

func (b *backend) roleList(ctx context.Context, req *logical.Request, _ *framework.FieldData) (*logical.Response, error) {
	names, err := req.Storage.List(ctx, rolePrefix)
	if err != nil {
		return nil, err
	}
	return logical.ListResponse(names), nil
}

func lowerAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func (b *backend) roleWrite(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	name := d.Get("name").(string)
	b.lock.Lock()
	defer b.lock.Unlock()
	r, err := getRole(ctx, req.Storage, name)
	if err != nil {
		return nil, err
	}
	if r == nil {
		r = &roleEntry{}
	}
	if v, ok := d.GetOk("bound_dids"); ok {
		r.BoundDIDs = nil
		for _, did := range v.([]string) {
			did = strings.TrimSpace(did)
			if did != "*" && !didPattern.MatchString(did) {
				return logical.ErrorResponse(fmt.Sprintf("bound_dids entry %q is not a DID", did)), nil
			}
			if did != "" {
				r.BoundDIDs = append(r.BoundDIDs, did)
			}
		}
	}
	if v, ok := d.GetOk("bound_handles"); ok {
		r.BoundHandles = nil
		for _, h := range lowerAll(v.([]string)) {
			if h != "*" {
				var err error
				if h, err = normalizeHandle(h); err != nil {
					return logical.ErrorResponse("bound_handles entry: " + err.Error()), nil
				}
			}
			r.BoundHandles = append(r.BoundHandles, h)
		}
	}
	if len(r.BoundDIDs) == 0 && len(r.BoundHandles) == 0 {
		return logical.ErrorResponse("bound_dids or bound_handles is required (use * to admit everyone)"), nil
	}
	if err := r.ParseTokenFields(req, d); err != nil {
		return logical.ErrorResponse(err.Error()), logical.ErrInvalidRequest
	}
	entry, err := logical.StorageEntryJSON(rolePrefix+name, r)
	if err != nil {
		return nil, err
	}
	if err := req.Storage.Put(ctx, entry); err != nil {
		return nil, err
	}
	return nil, nil
}

func (b *backend) roleRead(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	r, err := getRole(ctx, req.Storage, d.Get("name").(string))
	if err != nil || r == nil {
		return nil, err
	}
	data := map[string]interface{}{"bound_dids": r.BoundDIDs, "bound_handles": r.BoundHandles}
	r.PopulateTokenData(data)
	return &logical.Response{Data: data}, nil
}

func (b *backend) roleDelete(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	b.lock.Lock()
	defer b.lock.Unlock()
	return nil, req.Storage.Delete(ctx, rolePrefix+d.Get("name").(string))
}
