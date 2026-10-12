package marketnetwork

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
)

const (
	netpolServer  = "netpol-server"
	netpolClient  = "netpol-client"
	netpolPolicy  = "netpol-deny-ingress"
	netpolBody    = "netpol-ok"
	netpolPort    = 8080
	netpolSettle  = 120 * time.Second
	netpolHoldFor = 3
)

func netpolPodsManifest(node string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %[1]s
  namespace: default
  labels:
    app: %[1]s
spec:
  nodeName: %[3]s
  containers:
    - name: httpd
      image: %[4]s
      imagePullPolicy: IfNotPresent
      command: ["sh", "-c", "mkdir -p /www && echo %[5]s > /www/index.html && exec httpd -f -p %[6]d -h /www"]
---
apiVersion: v1
kind: Pod
metadata:
  name: %[2]s
  namespace: default
  labels:
    app: %[2]s
spec:
  nodeName: %[3]s
  containers:
    - name: client
      image: %[4]s
      imagePullPolicy: IfNotPresent
      command: ["sleep", "86400"]
`, netpolServer, netpolClient, node, m3WorkloadImage, netpolBody, netpolPort)
}

func netpolDenyManifest() string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: %s
  namespace: default
spec:
  podSelector:
    matchLabels:
      app: %s
  policyTypes: ["Ingress"]
`, netpolPolicy, netpolServer)
}

func (b *m3Network) netpolFetch(ip string) (string, error) {
	return b.tenant("exec", netpolClient, "-n", "default", "--", "wget", "-T", "3", "-qO-",
		fmt.Sprintf("http://%s:%d/", ip, netpolPort)).exec()
}

func (b *m3Network) netpolGuestReading(console string) string {
	var out strings.Builder
	probe, err := b.tenant("exec", netpolClient, "-n", "default", "--", "sh", "-c",
		"echo kernel.modprobe=$(cat /proc/sys/kernel/modprobe); grep -E '^(nf_tables|nft_[a-z_0-9]+|nfnetlink_log|xt_[A-Za-z_]+) ' /proc/modules | cut -d' ' -f1 | sort | tr '\\n' ' '").exec()
	fmt.Fprintf(&out, "guest kernel, read from the client pod (err %v): %s\n", err, probe)
	data, _ := os.ReadFile(console)
	for _, l := range strings.Split(string(data), "\n") {
		for _, mark := range []string{"Aborting sync", "Failed to load kernel module", "iptables-restore", "RULE_APPEND"} {
			if strings.Contains(l, mark) {
				fmt.Fprintf(&out, "console: %s\n", strings.TrimSpace(l))
				break
			}
		}
	}
	return out.String()
}

func (b *m3Network) assertNetworkPolicyEnforced(node, console string) {
	t := b.t
	b.tenant("apply", "-f", "-").stdin(netpolPodsManifest(node)).must()
	t.Cleanup(func() {
		_, _ = b.tenant("delete", "networkpolicy", netpolPolicy, "-n", "default", "--ignore-not-found").exec()
		_, _ = b.tenant("delete", "pod", netpolServer, netpolClient, "-n", "default", "--ignore-not-found", "--wait=false").exec()
	})
	var ip string
	b.waitTenant("netpol server and client Running on "+node, 3*time.Minute, func() bool {
		out := b.tenant("get", "pods", "-n", "default", netpolServer, netpolClient, "-o",
			`jsonpath={range .items[*]}{.status.phase} {end}`).out()
		ip = b.tenant("get", "pod", netpolServer, "-n", "default", "-o", "jsonpath={.status.podIP}").out()
		return slices.Equal(strings.Fields(out), []string{"Running", "Running"}) && ip != ""
	})
	b.reading("netpol server %s (%s) and client %s Running on %s", netpolServer, ip, netpolClient, node)

	var last string
	var lastErr error
	b.waitTenant("the client reaching the server with no NetworkPolicy", netpolSettle, func() bool {
		last, lastErr = b.netpolFetch(ip)
		return lastErr == nil && strings.TrimSpace(last) == netpolBody
	})
	b.reading("with no NetworkPolicy the client fetched %q from %s:%d", netpolBody, ip, netpolPort)

	b.tenant("apply", "-f", "-").stdin(netpolDenyManifest()).must()
	appliedAt := time.Now()
	refused := 0
	end := time.Now().Add(netpolSettle)
	for refused < netpolHoldFor {
		last, lastErr = b.netpolFetch(ip)
		if lastErr == nil {
			refused = 0
		} else {
			refused++
		}
		if refused < netpolHoldFor && time.Now().After(end) {
			t.Fatalf("a deny-all ingress NetworkPolicy on %s did not stop the client within %s: the last fetch "+
				"answered %q (err %v), so tenant NetworkPolicies are not enforced on this market node\n%s",
				netpolServer, netpolSettle, last, lastErr, b.netpolGuestReading(console))
		}
		time.Sleep(2 * time.Second)
	}
	b.reading("deny-all ingress NetworkPolicy %s enforced %s after it was applied: %d fetches in a row refused (last: %s)",
		netpolPolicy, time.Since(appliedAt).Round(time.Second), netpolHoldFor, strings.ReplaceAll(last, "\n", " "))

	b.tenant("delete", "networkpolicy", netpolPolicy, "-n", "default").must()
	b.waitTenant("the client reaching the server again once the NetworkPolicy is deleted", netpolSettle, func() bool {
		last, lastErr = b.netpolFetch(ip)
		return lastErr == nil && strings.TrimSpace(last) == netpolBody
	})
	b.reading("NetworkPolicy deleted: the client fetches %q again, so the refusal was the policy", netpolBody)
	b.reading("%s", strings.TrimSpace(b.netpolGuestReading(console)))

	b.tenant("delete", "pod", netpolServer, netpolClient, "-n", "default", "--wait=true", "--timeout=2m").must()
}
