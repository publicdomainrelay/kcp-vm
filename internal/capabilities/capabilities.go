package capabilities

import (
	"fmt"
	"strings"
)

type Need string

const (
	NeedHostBackend Need = "host-backend"
	NeedProc        Need = "proc"
	NeedSystemdUser Need = "systemd-user"
	NeedVMRuntime   Need = "vm-runtime"
)

type Verb struct {
	Path    string
	Summary string
	BuiltBy string
	Needs   []Need
	Hidden  bool
}

func (v Verb) Built() bool { return v.BuiltBy == "" }

func (v Verb) Words() []string { return strings.Fields(v.Path) }

const (
	PlanWindows = "plan 0011"
	PlanDarwin  = "plan 0012"
)

var available = map[string]map[Need]bool{
	"linux": {NeedHostBackend: true, NeedProc: true, NeedSystemdUser: true, NeedVMRuntime: true},
}

var needNames = map[Need]string{
	NeedHostBackend: "no placement backend",
	NeedProc:        "no proc placement backend",
	NeedSystemdUser: "no systemd user manager",
	NeedVMRuntime:   "no VM runtime",
}

func planFor(goos string) string {
	switch goos {
	case "windows":
		return PlanWindows
	case "darwin":
		return PlanDarwin
	}
	return "no plan names " + goos
}

func Missing(v Verb, goos, goarch string) (string, bool) {
	for _, n := range v.Needs {
		if !available[goos][n] {
			return fmt.Sprintf("%s: %s on %s/%s in this build (%s)", v.Path, needNames[n], goos, goarch, planFor(goos)), true
		}
	}
	return "", false
}

func Tree() []Verb {
	return []Verb{
		{Path: "version", Summary: "print the build, its Go and its target (--json)"},
		{Path: "dev up", Summary: "start (or adopt) an application instance under devsup: kine, a port-0 front and one kcp slot behind it (--profile minimal; --fg binds it to this terminal)", Needs: []Need{NeedProc}},
		{Path: "dev host", Summary: "start (or adopt) the user's parent instance (dev-host): its own bootstrap layer under its own devsup; its root:dev:<instance> workspaces hold each child's HostProcess objects (dev up --parent dev-host)", Needs: []Need{NeedProc}},
		{Path: "dev status", Summary: "the instance record and its devsup units", Needs: []Need{NeedProc}},
		{Path: "dev down", Summary: "stop every unit of the instance and remove its state (--keep-state keeps it)", Needs: []Need{NeedProc}},
		{Path: "dev ls", Summary: "every dev instance under the state root, with its devsup reachability", Needs: []Need{NeedProc}},
		{Path: "dev logs", Summary: "a unit's log: dev logs <kine-a|front|kcp-a|kcp-b|core|devsup> [--tail N]", Needs: []Need{NeedProc}},
		{Path: "dev env", Summary: "exports for shells and TS tools (KUBECONFIG, kcp URL, ports, leases; --json)", Needs: []Need{NeedProc}},
		{Path: "dev check", Summary: "report the dev host prerequisites; never fixes them", Needs: []Need{NeedProc}},
		{Path: "dev restart", Summary: "dev restart core [--binary path]: replace the dev core, adopting every other unit", Needs: []Need{NeedProc}},
		{Path: "dev roll", Summary: "roll a tenant cluster's control plane in a dev instance", BuiltBy: "M3-CP-SLOTS", Needs: []Need{NeedProc}},
		{Path: "dev roll kcp", Summary: "blue/green kcp slot swap behind the instance's front", Needs: []Need{NeedProc}},
		{Path: "dev gc", Summary: "collect the per-user asset cache (refused while a devsup of this user runs)", Needs: []Need{NeedProc}},
		{Path: "serve gateway kcp", Summary: "the gateway application instance", BuiltBy: "M3-SERVE-GATEWAY-KCP", Needs: []Need{NeedHostBackend}},
		{Path: "serve gateway xrpc", Summary: "the XRPC gateway (socialweb-gateway; SOCIALWEB_GATEWAY_* env)"},
		{Path: "serve provider", Summary: "the cluster provider (socialweb-clusterprovider; SOCIALWEB_CLUSTERPROVIDER_* env)"},
		{Path: "run bidder", Summary: "a bidder application instance", BuiltBy: "plan 0007 M5-RUN-BIDDER", Needs: []Need{NeedHostBackend, NeedVMRuntime}},
		{Path: "run gateway ssh", Summary: "ssh to a guest through the relay", BuiltBy: "plan 0008 M4-RUN-GATEWAY-SSH"},
		{Path: "run request", Summary: "a requester application instance (RFP flow)", BuiltBy: "plan 0008 M4-RUN-REQUEST"},
		{Path: "vault prepare-root", Summary: "prepare the root vault (socialweb-vault prepare-root)"},
		{Path: "vault apply-root", Summary: "apply the root vault's backends, roles and policies (socialweb-vault apply-root)"},
		{Path: "migrate", Summary: "apply the clusterprovider migrations (socialweb-clusterprovider migrate)"},
		{Path: "apply", Summary: "server-side apply manifest sets into kcp workspaces (--set a,b --values file --dry-run; --instance or --kcp-kubeconfig)"},
		{Path: "manifests list", Summary: "list the embedded manifest sets, their phases and the YAML no set applies (--json)"},
		{Path: "manifests render", Summary: "render manifest sets with typed values (--set --values --phase)"},
		{Path: "check-host", Summary: "probe the Linux host contract (socialweb-computer-kcp-check-host)", Needs: []Need{NeedSystemdUser}},
		{Path: "install gateway", Summary: "the one explicit root step for a gateway", Needs: []Need{NeedSystemdUser}},
		{Path: "install bidder", Summary: "the one explicit root step for a bidder", Needs: []Need{NeedSystemdUser}},
		{Path: "uninstall gateway", Summary: "undo install gateway", Needs: []Need{NeedSystemdUser}},
		{Path: "uninstall bidder", Summary: "undo install bidder", Needs: []Need{NeedSystemdUser}},
		{Path: "upgrade", Summary: "content-addressed swap of the core binary: --binary, --sha256; self-check, retarget bin/current, restart the core units (as the account)"},
		{Path: "rollback", Summary: "retarget bin/current to bin/previous and restart the core units (as the account)"},
		{Path: "units list", Summary: "list this account's units", Needs: []Need{NeedHostBackend}},
		{Path: "units audit", Summary: "audit VM process trees: only pinned VM-unit asset executables may run in a VM unit (exit 1 naming the unit and exe)", Needs: []Need{NeedHostBackend}},
		{Path: "units gc", Summary: "collect failed and orphaned units", BuiltBy: "M3-CLUSTER-UNITS", Needs: []Need{NeedHostBackend}},
		{Path: "units roll", Summary: "roll kcp or a cluster's control plane", BuiltBy: "M3-KCP-ROLL", Needs: []Need{NeedHostBackend}},
		{Path: "internal devsup", Summary: "the per-instance proc backend supervisor", Needs: []Need{NeedProc}, Hidden: true},
		{Path: "internal dev-core", Summary: "the dev instance's core: adopts the bootstrap units and records them", Needs: []Need{NeedProc}, Hidden: true},
		{Path: "internal ns-exec", Summary: "private hostname one-shot for the proc backend", BuiltBy: "M3-CP-SLOTS", Hidden: true},
		{Path: "internal vm start", Summary: "prepare and start one VM unit (spec JSON on stdin; prints its runtime record)", Needs: []Need{NeedHostBackend, NeedVMRuntime}, Hidden: true},
		{Path: "internal vm inspect", Summary: "the verdict of one VM: Running, Lost (a mismatch is stopped now) or Unknown", Needs: []Need{NeedHostBackend, NeedVMRuntime}, Hidden: true},
		{Path: "internal vm stop", Summary: "stop one VM (CtrlAltDel, the grace, then SIGKILL) and remove its work dir", Needs: []Need{NeedHostBackend, NeedVMRuntime}, Hidden: true},
		{Path: "internal vm list", Summary: "the VM units and work dirs of one handle", Needs: []Need{NeedHostBackend, NeedVMRuntime}, Hidden: true},
		{Path: "internal apiserver-front", Summary: "the L4 front of kcp or a tenant apiserver", Hidden: true},
		{Path: "internal cloud-controller-manager", Summary: "the tenant CCM, re-exec'd by the cluster provider", Hidden: true},
		{Path: "internal tenantcredential", Summary: "mint a tenant credential (socialweb-tenantcredential)", Hidden: true},
	}
}

func Find(args []string) (Verb, []string, bool) {
	var best Verb
	n := 0
	for _, v := range Tree() {
		w := v.Words()
		if len(w) > len(args) || len(w) <= n {
			continue
		}
		match := true
		for i := range w {
			if w[i] != args[i] {
				match = false
				break
			}
		}
		if match {
			best, n = v, len(w)
		}
	}
	return best, args[n:], n > 0
}

func Groups() map[string]bool {
	out := map[string]bool{}
	for _, v := range Tree() {
		w := v.Words()
		for i := 1; i < len(w); i++ {
			out[strings.Join(w[:i], " ")] = true
		}
	}
	return out
}
