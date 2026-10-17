//go:build linux

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type networkView struct {
	Network struct {
		Status struct {
			Listeners []struct {
				Component string `json:"component"`
				Address   string `json:"address"`
			} `json:"listeners"`
			Network struct {
				CaBundleRef string `json:"caBundleRef"`
			} `json:"network"`
			Apps []struct {
				Did string `json:"did"`
			} `json:"apps"`
		} `json:"status"`
	} `json:"network"`
}

func (n networkView) dids() []string {
	var out []string
	for _, a := range n.Network.Status.Apps {
		if a.Did != "" {
			out = append(out, a.Did)
		}
	}
	return out
}

func kvSample(stateDir string) map[string]any {
	out := map[string]any{}
	var paths []string
	for _, pat := range []string{"network/state/*/kv.sqlite3", "apps/dev/*/components/*/pds", "apps/dev/*/components/*/*/kv.sqlite3"} {
		m, _ := filepath.Glob(filepath.Join(stateDir, pat))
		paths = append(paths, m...)
	}
	for _, p := range paths {
		rel, _ := filepath.Rel(stateDir, p)
		entry := map[string]any{}
		if info, err := os.Stat(p); err == nil {
			entry["bytes"] = info.Size()
		}
		if info, err := os.Stat(p + "-wal"); err == nil {
			entry["walBytes"] = info.Size()
		}
		if db, err := sql.Open("sqlite", "file:"+p+"?mode=ro&_pragma=busy_timeout(5000)"); err == nil {
			var n int64
			if err := db.QueryRow("select count(*) from kv").Scan(&n); err == nil {
				entry["rows"] = n
			} else {
				entry["rowsError"] = err.Error()
			}
			db.Close()
		}
		out[rel] = entry
	}
	return out
}

func pdsSample(ctx context.Context, binary, instance, stateDir string) map[string]any {
	out := map[string]any{}
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(cctx, binary, "dev", "status", "--instance", instance, "--state-dir", stateDir).Output()
	var nv networkView
	if err != nil || json.Unmarshal(raw, &nv) != nil {
		return map[string]any{"error": fmt.Sprint(err)}
	}
	ca, err := os.ReadFile(nv.Network.Status.Network.CaBundleRef)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	get := func(base, path string, into any) error {
		resp, err := client.Get(base + path)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		return json.NewDecoder(resp.Body).Decode(into)
	}
	for _, l := range nv.Network.Status.Listeners {
		if !strings.HasSuffix(l.Component, "/pds") {
			continue
		}
		base := "https://" + l.Address
		perRepo := map[string]any{}
		for _, did := range nv.dids() {
			r := struct{ Did string }{did}
			var desc struct {
				Collections []string `json:"collections"`
			}
			if err := get(base, "/xrpc/com.atproto.repo.describeRepo?repo="+r.Did, &desc); err != nil || len(desc.Collections) == 0 {
				continue
			}
			counts := map[string]int{}
			for _, c := range desc.Collections {
				cursor := ""
				for {
					var page struct {
						Records []json.RawMessage `json:"records"`
						Cursor  string            `json:"cursor"`
					}
					path := "/xrpc/com.atproto.repo.listRecords?limit=100&repo=" + r.Did + "&collection=" + c
					if cursor != "" {
						path += "&cursor=" + cursor
					}
					if err := get(base, path, &page); err != nil {
						break
					}
					counts[strings.TrimPrefix(c, "com.publicdomainrelay.temp.")] += len(page.Records)
					if page.Cursor == "" || len(page.Records) == 0 {
						break
					}
					cursor = page.Cursor
				}
			}
			perRepo[r.Did] = counts
		}
		out[l.Component] = perRepo
	}
	return out
}
