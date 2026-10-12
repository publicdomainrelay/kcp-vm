package openbao

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type Mount struct {
	Type    string         `json:"type"`
	Options map[string]any `json:"options"`
}

func (c *Client) SecretMounts(ctx context.Context) (map[string]Mount, error) {
	return c.mounts(ctx, "sys/mounts")
}

func (c *Client) AuthMounts(ctx context.Context) (map[string]Mount, error) {
	return c.mounts(ctx, "sys/auth")
}

func (c *Client) mounts(ctx context.Context, path string) (map[string]Mount, error) {
	raw, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]Mount{}
	if len(raw) == 0 {
		return out, nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("openbao: GET /v1/%s returned a body that is not JSON: %v", path, err)
	}
	for name, entry := range entries {
		var m Mount
		if json.Unmarshal(entry, &m) != nil || m.Type == "" {
			continue
		}
		out[strings.Trim(name, "/")] = m
	}
	return out, nil
}

func (c *Client) DisableMount(ctx context.Context, path string) error {
	_, err := c.do(ctx, http.MethodDelete, "sys/mounts/"+strings.Trim(path, "/"), nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (c *Client) TuneMount(ctx context.Context, path string, body map[string]any) error {
	_, err := c.do(ctx, http.MethodPost, "sys/mounts/"+strings.Trim(path, "/")+"/tune", body)
	return err
}

func (c *Client) CAPEM(ctx context.Context, pkiMount string) (string, error) {
	path := strings.Trim(pkiMount, "/") + "/ca/pem"
	status, body, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", &ResponseError{Method: http.MethodGet, Path: path, Status: status, Body: strings.TrimSpace(string(body))}
	}
	pem := strings.TrimSpace(string(body))
	if pem == "" {
		return "", fmt.Errorf("openbao: %s holds no certificate", path)
	}
	return pem + "\n", nil
}

func (c *Client) EnableAuth(ctx context.Context, path, kind string) error {
	_, err := c.do(ctx, http.MethodPut, "sys/auth/"+strings.Trim(path, "/"), map[string]any{"type": kind})
	return err
}

func (c *Client) DisableAuth(ctx context.Context, path string) error {
	_, err := c.do(ctx, http.MethodDelete, "sys/auth/"+strings.Trim(path, "/"), nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (c *Client) Write(ctx context.Context, path string, body map[string]any) error {
	_, err := c.do(ctx, http.MethodPost, strings.Trim(path, "/"), body)
	return err
}

func (c *Client) Read(ctx context.Context, path string) (map[string]any, error) {
	raw, err := c.do(ctx, http.MethodGet, strings.Trim(path, "/"), nil)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if len(raw) == 0 || string(raw) == "null" {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("openbao: reading %q returned data that is not a JSON object: %v", path, err)
	}
	return out, nil
}

func (c *Client) Delete(ctx context.Context, path string) error {
	_, err := c.do(ctx, http.MethodDelete, strings.Trim(path, "/"), nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

func (c *Client) DeletePolicy(ctx context.Context, name string) error {
	return c.Delete(ctx, "sys/policies/acl/"+strings.Trim(name, "/"))
}

func (c *Client) List(ctx context.Context, path string) ([]string, error) {
	raw, err := c.do(ctx, "LIST", strings.Trim(path, "/"), nil)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var payload struct {
		Keys []string `json:"keys"`
	}
	if len(raw) == 0 {
		return nil, nil
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("openbao: listing %q returned a body that is not JSON: %v", path, err)
	}
	return payload.Keys, nil
}

func (c *Client) LoginJWT(ctx context.Context, mount, role, jwt string) (string, error) {
	path := "auth/" + strings.Trim(mount, "/") + "/login"
	status, body, err := c.request(ctx, http.MethodPost, path, map[string]any{"role": role, "jwt": jwt})
	if err != nil {
		return "", err
	}
	if status < 200 || status > 299 {
		return "", &ResponseError{Method: http.MethodPost, Path: path, Status: status, Body: strings.TrimSpace(string(body))}
	}
	var payload struct {
		Auth struct {
			ClientToken string `json:"client_token"`
		} `json:"auth"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("openbao: %s returned a body that is not JSON: %v (%s)", path, err, strings.TrimSpace(string(body)))
	}
	if payload.Auth.ClientToken == "" {
		return "", fmt.Errorf("openbao: %s answered without a client token (%s)", path, strings.TrimSpace(string(body)))
	}
	return payload.Auth.ClientToken, nil
}
