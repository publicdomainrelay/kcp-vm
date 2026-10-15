package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const maxBodyBytes = 1 << 20

var nonPublicPrefixes = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
	"224.0.0.0/4", "240.0.0.0/4", "::/128", "::1/128", "64:ff9b::/96", "100::/64", "2001:db8::/32",
	"fc00::/7", "fe80::/10", "ff00::/8",
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	for _, p := range nonPublicPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

func (c *configEntry) trusts(hostport string) bool {
	_, ok := c.trustedAddr(hostport)
	return ok
}

func (c *configEntry) trustedAddr(hostport string) (string, bool) {
	hostport = strings.ToLower(hostport)
	if addr, ok := c.TrustedHosts[hostport]; ok {
		return addr, true
	}
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		if addr, ok := c.TrustedHosts[host]; ok {
			return addr, true
		}
	}
	return "", false
}

type fetcher struct {
	cfg     *configEntry
	guarded *http.Client
	trusted *http.Client
}

func newFetcher(cfg *configEntry) (*fetcher, error) {
	pool, err := cfg.trustedPool()
	if err != nil {
		return nil, err
	}
	noRedirect := func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	guarded := &http.Transport{
		DialContext:         cfg.guardedDial,
		TLSHandshakeTimeout: cfg.CallTimeout,
		DisableKeepAlives:   true,
		Proxy:               nil,
	}
	trusted := &http.Transport{
		DialContext:         cfg.trustedDial,
		TLSClientConfig:     &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: cfg.CallTimeout,
		DisableKeepAlives:   true,
		Proxy:               nil,
	}
	return &fetcher{
		cfg:     cfg,
		guarded: &http.Client{Transport: guarded, CheckRedirect: noRedirect},
		trusted: &http.Client{Transport: trusted, CheckRedirect: noRedirect},
	}, nil
}

func (c *configEntry) guardedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []netip.Addr
	if ip, perr := netip.ParseAddr(host); perr == nil {
		ips = []netip.Addr{ip}
	} else {
		found, lerr := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if lerr != nil {
			return nil, lerr
		}
		ips = found
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("host %q has no address", host)
	}
	for _, ip := range ips {
		if !publicAddr(ip) {
			return nil, fmt.Errorf("network rule: host %q resolves to %s, which is not a public address; only trusted_hosts may reach non-public addresses", host, ip.Unmap())
		}
	}
	var last error
	d := &net.Dialer{}
	for _, ip := range ips {
		conn, derr := d.DialContext(ctx, network, net.JoinHostPort(ip.Unmap().String(), port))
		if derr == nil {
			return conn, nil
		}
		last = derr
	}
	return nil, last
}

func (c *configEntry) trustedDial(ctx context.Context, network, addr string) (net.Conn, error) {
	if mapped, ok := c.trustedAddr(addr); ok && mapped != "" {
		addr = mapped
	}
	return (&net.Dialer{}).DialContext(ctx, network, addr)
}

func (f *fetcher) clientFor(u *url.URL) (*http.Client, error) {
	if f.cfg.trusts(u.Host) {
		return f.trusted, nil
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("network rule: %q is not https; only trusted_hosts may use another scheme", u.String())
	}
	return f.guarded, nil
}

func (f *fetcher) checkURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("%q is not a URL without userinfo and fragment", raw)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && f.cfg.trusts(u.Host)) {
		return nil, fmt.Errorf("network rule: %q is not https; only trusted_hosts may use another scheme", raw)
	}
	return u, nil
}

func (f *fetcher) do(ctx context.Context, req *http.Request) (*http.Response, []byte, error) {
	client, err := f.clientFor(req.URL)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, f.cfg.CallTimeout)
	defer cancel()
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(body) > maxBodyBytes {
		return nil, nil, fmt.Errorf("response from %s is larger than %d bytes", req.URL.Host, maxBodyBytes)
	}
	return resp, body, nil
}

func (f *fetcher) get(ctx context.Context, raw string, accept string) ([]byte, error) {
	u, err := f.checkURL(raw)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, body, err := f.do(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: status %d", raw, resp.StatusCode)
	}
	return body, nil
}

func originOf(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("not an absolute URL")
	}
	return u.Scheme + "://" + u.Host, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
