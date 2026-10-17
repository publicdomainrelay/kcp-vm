//go:build integration

package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
)

const (
	coreRestartWait     = 4 * time.Minute
	cookieBackWait      = time.Minute
	clusterListBackWait = 3 * time.Minute
	kcpDumpMinBytes     = 100_000
	kcpDumpMinSpaces    = 4
	kcpDumpTimeout      = 5 * time.Minute
)

type coreRestart struct {
	Binary struct {
		Path string `json:"path"`
	} `json:"binary"`
	Before struct {
		PID          int    `json:"pid"`
		InvocationID string `json:"invocationID"`
	} `json:"before"`
	After struct {
		PID          int    `json:"pid"`
		InvocationID string `json:"invocationID"`
	} `json:"after"`
	UnitsUnchanged bool    `json:"unitsUnchanged"`
	Seconds        float64 `json:"seconds"`
}

func TestWebAppCookieSurvivesAGatewayCoreKillAndACoreBinarySwapAndNoSecretLeaks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { front.Close() })
	origin := "https://" + front.Addr().String()
	n := StartNetwork(t, NetworkOptions{Bidders: 1, WithGateway: true, UpTimeout: 5 * time.Minute,
		GatewayOptions: map[string]string{"public-origin": origin}})
	tn := n.Tenant(t, "tenant", tenantReady)
	waitOwnerGrant(t, n, tn)
	acct := n.OwnerAccount(t)
	web := n.webSignIn(t, front, origin, acct)

	swap := filepath.Join(n.Dir, "upgrade", "socialweb-computer")
	if r := n.h.As(ctx, "install", "-D", "-m", "0755", n.h.Bin, swap); r.Err != nil {
		t.Fatalf("copying the binary for the swap: %v %s", r.Err, r.Tail(3))
	}
	for _, leg := range []struct {
		name string
		do   func() (string, error)
	}{
		{"kill -9 of the gateway core", func() (string, error) {
			before, err := n.coreOf(n.GatewayDir())
			if err != nil {
				return "", err
			}
			if err := n.signal("KILL", before); err != nil {
				return "", err
			}
			for deadline := time.Now().Add(coreRestartWait); ; time.Sleep(time.Second) {
				if after, err := n.coreOf(n.GatewayDir()); err == nil && after != before {
					return fmt.Sprintf("gateway core pid %d -> %d", before, after), nil
				}
				if herr := n.health(); herr != nil {
					return "", herr
				}
				if time.Now().After(deadline) {
					return "", fmt.Errorf("no new gateway core within %s of the kill of %d", coreRestartWait, before)
				}
			}
		}},
		{"the network core restarted on another binary", func() (string, error) {
			r := n.Run("dev-restart-core", coreRestartWait, "dev", "restart", "--instance", n.Instance, "--state-dir", n.StateRoot, "--binary", swap, "core")
			var rep coreRestart
			if err := json.Unmarshal([]byte(r.Stdout), &rep); err != nil || r.Err != nil {
				return "", fmt.Errorf("%v %v\n%s", r.Err, err, r.Tail(10))
			}
			if rep.Before.PID == 0 || rep.After.PID == rep.Before.PID || rep.After.InvocationID == rep.Before.InvocationID || !rep.UnitsUnchanged || rep.Binary.Path != swap {
				return "", fmt.Errorf("the core was not replaced from %s with every unit adopted: %+v", swap, rep)
			}
			return fmt.Sprintf("network core pid %d -> %d on %s in %.1fs, every unit adopted", rep.Before.PID, rep.After.PID, rep.Binary.Path, rep.Seconds), nil
		}},
	} {
		what, err := leg.do()
		if err != nil {
			t.Fatalf("%s: %v", leg.name, err)
		}
		back := time.Now()
		for deadline := back.Add(cookieBackWait); ; time.Sleep(500 * time.Millisecond) {
			if herr := n.health(); herr != nil {
				t.Fatalf("%s: %v", leg.name, herr)
			}
			code, _, raw, err := web.try(http.MethodGet, "/session", nil)
			if err == nil && code == http.StatusOK && strings.Contains(string(raw), acct.DID) {
				break
			}
			if code == http.StatusUnauthorized {
				t.Fatalf("%s: the same cookie got 401 from GET /session: a new sign-in would be needed", leg.name)
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: GET /session with the same cookie: %d %.100s %v", leg.name, code, raw, err)
			}
		}
		session := time.Since(back)
		for deadline := time.Now().Add(clusterListBackWait); ; time.Sleep(time.Second) {
			code, _, body, err := web.try(http.MethodPost, "/api/clusters/list", map[string]any{})
			if err == nil && code == http.StatusOK && strings.Contains(string(body), `"tenant"`) {
				break
			}
			if herr := n.health(); herr != nil {
				t.Fatalf("%s: %v", leg.name, herr)
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the cluster list through the same cookie for %s: %d %.200s %v", leg.name, clusterListBackWait, code, body, err)
			}
		}
		t.Logf("%s: %s; the same cookie answered GET /session %.1fs and the cluster list %.1fs later", leg.name, what, session.Seconds(), time.Since(back).Seconds())
	}

	code, _, body := web.do(http.MethodPost, "/api/clusters/kubeconfig", map[string]any{"name": "tenant"})
	var download struct {
		Kubeconfig string `json:"kubeconfig"`
	}
	if err := json.Unmarshal(body, &download); err != nil || code != http.StatusOK || download.Kubeconfig == "" {
		t.Fatalf("the kubeconfig download after the core restarts: %d %v", code, err)
	}
	cfg, err := clientcmd.Load([]byte(download.Kubeconfig))
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{web.token}
	for _, ai := range cfg.AuthInfos {
		if len(ai.ClientKeyData) == 0 {
			t.Fatal("the downloaded kubeconfig has no client key")
		}
		secrets = append(secrets, base64.StdEncoding.EncodeToString(ai.ClientKeyData))
		for _, l := range strings.Split(string(ai.ClientKeyData), "\n") {
			if len(l) == 64 {
				secrets = append(secrets, l)
				break
			}
		}
	}
	if len(secrets) < 3 {
		t.Fatalf("the downloaded client key gave no PEM body line to look for")
	}

	files, err := n.FilesContaining([]string{n.StateDir}, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) > 0 {
		t.Errorf("the session cookie or the downloaded client key is in files of the network: %v", files)
	}
	if c, err := n.JournalContains(secrets, n.Started); err != nil || c > 0 {
		t.Errorf("the session cookie or the downloaded client key is in %d journal line(s) of the account: %v", c, err)
	}
	if procs, err := n.processesMentioning(secrets...); err != nil || len(procs) > 0 {
		t.Errorf("the session cookie or the downloaded client key is in the arguments or environment of: %v %v", procs, err)
	}

	dump := filepath.Join(n.Dir, "kcp-dump")
	dumpCtx, cancelDump := context.WithTimeout(ctx, kcpDumpTimeout)
	defer cancelDump()
	listing, err := n.h.Command(dumpCtx, "sh", "-c", kcpDumpScript, "sh", n.StateDir, dump, kubectlBinary).Output()
	if err != nil {
		t.Fatalf("dumping every kcp workspace: %v", err)
	}
	var spaces, dumped int
	for _, l := range strings.Split(strings.TrimSpace(string(listing)), "\n") {
		var ws string
		var size int
		if _, err := fmt.Sscanf(l, "%s %d", &ws, &size); err == nil {
			spaces++
			dumped += size
		}
	}
	if spaces < kcpDumpMinSpaces || dumped < kcpDumpMinBytes {
		t.Fatalf("the kcp dump read too little: %d workspace(s), %d bytes:\n%s", spaces, dumped, listing)
	}
	hits, err := n.FilesContaining([]string{dump}, append(secrets, `"refresh_token"`, "dpopPrivate", "client-key-data"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) > 0 {
		t.Errorf("kcp objects hold the session cookie, the client key, a refresh token or a DPoP key: %v", hits)
	}
	t.Logf("scanned %d kcp workspace(s) of every kcp of the network (%d bytes of objects), the state dir, the journal and every process", spaces, dumped)

	bases, dids := []string{acct.pds}, map[string]bool{acct.DID: true}
	for _, h := range n.Up.Network.Status.Network.Hosts {
		base := "https://" + h.Address
		bases = append(bases, base)
		out, code, err := postJSON(acct.client, base+"/xrpc/com.atproto.sync.listRepos?limit=1000", "", nil)
		if err != nil || code != http.StatusOK {
			continue
		}
		list, _ := out["repos"].([]any)
		for _, rp := range list {
			if did, _ := rp.(map[string]any)["did"].(string); did != "" {
				dids[did] = true
			}
		}
	}
	records, repos := 0, map[string]bool{}
	for did := range dids {
		for _, base := range bases {
			if repos[did] {
				break
			}
			desc, code, err := postJSON(acct.client, base+"/xrpc/com.atproto.repo.describeRepo?repo="+url.QueryEscape(did), "", nil)
			if err != nil || code != http.StatusOK || desc["did"] != did {
				continue
			}
			repos[did] = true
			cols, _ := desc["collections"].([]any)
			for _, c := range cols {
				col, _ := c.(string)
				cursor := ""
				for {
					q := url.Values{"repo": {did}, "collection": {col}, "limit": {"100"}}
					if cursor != "" {
						q.Set("cursor", cursor)
					}
					page, code, err := postJSON(acct.client, base+"/xrpc/com.atproto.repo.listRecords?"+q.Encode(), "", nil)
					if err != nil || code != http.StatusOK {
						t.Fatalf("listRecords %s %s on %s: %d %v", did, col, base, code, err)
					}
					raw, _ := json.Marshal(page)
					for _, s := range append(secrets, `"refresh_token"`, "dpopPrivate", "PRIVATE KEY") {
						if strings.Contains(string(raw), s) {
							t.Errorf("a record of %s in %s on %s holds a secret (%.12s...)", did, col, base, s)
						}
					}
					recs, _ := page["records"].([]any)
					records += len(recs)
					cursor, _ = page["cursor"].(string)
					if cursor == "" || len(recs) == 0 {
						break
					}
				}
			}
		}
	}
	if !repos[acct.DID] || records == 0 {
		t.Fatalf("the PDS scan did not read the owner's repository: %d of %d repo(s), %d record(s)", len(repos), len(dids), records)
	}
	t.Logf("scanned %d record(s) of %d of %d repo(s) the network lists", records, len(repos), len(dids))
}

func (n *Network) coreOf(dir string) (int, error) {
	r := n.h.As(context.Background(), "pgrep", "-f", "-u", fmt.Sprint(n.h.UID), "--", "[i]nternal dev-core --instance-dir "+dir+" ")
	fields := strings.Fields(r.Stdout)
	if len(fields) != 1 {
		return 0, fmt.Errorf("want one dev-core of %s, found %q", dir, strings.TrimSpace(r.Stdout))
	}
	var pid int
	_, err := fmt.Sscan(fields[0], &pid)
	return pid, err
}

const kcpDumpScript = `set -u
state="$1"; out="$2"; kubectl="$3"
mkdir -p -m 0700 "$out"
walk() {
  k="$1"; base="$2"; ws="$3"; tag="$4"
  f="$out/$tag.$(echo "$ws" | tr ':' '_').yaml"
  types="$("$kubectl" --kubeconfig "$k" --server "$base/clusters/$ws" api-resources --verbs=list -o name 2>/dev/null | paste -sd, -)"
  [ -n "$types" ] && "$kubectl" --kubeconfig "$k" --server "$base/clusters/$ws" get "$types" -A -o yaml >"$f" 2>/dev/null
  echo "$tag:$ws $(wc -c <"$f" 2>/dev/null || echo 0)"
  "$kubectl" --kubeconfig "$k" --server "$base/clusters/$ws" get workspaces -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' 2>/dev/null |
    while read -r c; do [ -n "$c" ] && walk "$k" "$base" "$ws:$c" "$tag"; done
}
i=0
find "$state" -name admin.kubeconfig -path '*/kcp/*' | while read -r k; do
  i=$((i + 1))
  server="$("$kubectl" config view --kubeconfig "$k" --raw -o jsonpath='{.clusters[0].cluster.server}')"
  walk "$k" "${server%%/clusters/*}" root "kcp$i"
done
`
