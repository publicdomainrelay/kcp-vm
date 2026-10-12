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
		{Path: "dev up", Summary: "start (or adopt) an application instance under devsup: kine + one kcp slot (--profile minimal)", Needs: []Need{NeedProc}},
		{Path: "dev status", Summary: "the instance record and its devsup units", Needs: []Need{NeedProc}},
		{Path: "dev down", Summary: "stop every unit of the instance and remove its state (--keep-state keeps it)", Needs: []Need{NeedProc}},
		{Path: "dev ls", Summary: "every dev instance of this user", BuiltBy: "M1-DEV-INSTANCE", Needs: []Need{NeedProc}},
		{Path: "dev logs", Summary: "a unit's log", BuiltBy: "M1-DEV-INSTANCE", Needs: []Need{NeedProc}},
		{Path: "dev env", Summary: "exports for shells and TS tools", BuiltBy: "M1-DEV-INSTANCE", Needs: []Need{NeedProc}},
		{Path: "dev check", Summary: "report the dev host prerequisites", BuiltBy: "M1-DEV-INSTANCE", Needs: []Need{NeedProc}},
		{Path: "dev restart", Summary: "replace the dev core, adopting every other unit", BuiltBy: "M1-DEV-INSTANCE", Needs: []Need{NeedProc}},
		{Path: "dev roll", Summary: "roll kcp or a cluster in a dev instance", BuiltBy: "M3-KCP-ROLL", Needs: []Need{NeedProc}},
		{Path: "dev gc", Summary: "collect the per-user asset cache", BuiltBy: "M1-ASSETS", Needs: []Need{NeedProc}},
		{Path: "serve gateway kcp", Summary: "the gateway application instance", BuiltBy: "M3-SERVE-GATEWAY-KCP", Needs: []Need{NeedHostBackend}},
		{Path: "serve provider", Summary: "the cluster provider", BuiltBy: "M3-FOLD", Needs: []Need{NeedHostBackend}},
		{Path: "run bidder", Summary: "a bidder application instance", BuiltBy: "plan 0007 M5-RUN-BIDDER", Needs: []Need{NeedHostBackend, NeedVMRuntime}},
		{Path: "run gateway ssh", Summary: "ssh to a guest through the relay", BuiltBy: "plan 0008 M4-RUN-GATEWAY-SSH"},
		{Path: "run request", Summary: "a requester application instance (RFP flow)", BuiltBy: "plan 0008 M4-RUN-REQUEST"},
		{Path: "vault prepare-root", Summary: "prepare the root vault", BuiltBy: "M3-BAO-SYNC"},
		{Path: "vault apply-root", Summary: "apply the root vault's backends, roles and policies", BuiltBy: "M3-BAO-SYNC"},
		{Path: "migrate", Summary: "apply the clusterprovider migrations", BuiltBy: "M3-FOLD"},
		{Path: "apply", Summary: "server-side apply a manifest set into kcp", BuiltBy: "M2-APPLY"},
		{Path: "manifests list", Summary: "list the embedded manifest sets", BuiltBy: "M2-MANIFESTS"},
		{Path: "manifests render", Summary: "render a manifest set", BuiltBy: "M2-MANIFESTS"},
		{Path: "check-host", Summary: "probe the Linux host contract", BuiltBy: "M2-HOST-CONTRACT", Needs: []Need{NeedSystemdUser}},
		{Path: "install gateway", Summary: "the one explicit root step for a gateway", BuiltBy: "M1-INSTALL", Needs: []Need{NeedSystemdUser}},
		{Path: "install bidder", Summary: "the one explicit root step for a bidder", BuiltBy: "M1-INSTALL", Needs: []Need{NeedSystemdUser}},
		{Path: "uninstall gateway", Summary: "undo install gateway", BuiltBy: "M1-INSTALL", Needs: []Need{NeedSystemdUser}},
		{Path: "uninstall bidder", Summary: "undo install bidder", BuiltBy: "M1-INSTALL", Needs: []Need{NeedSystemdUser}},
		{Path: "upgrade", Summary: "content-addressed swap of the core binary", BuiltBy: "M1-INSTALL"},
		{Path: "rollback", Summary: "swap back to the previous core binary", BuiltBy: "M1-INSTALL"},
		{Path: "units list", Summary: "list this account's units", BuiltBy: "M2-VM-TREE-AUDIT", Needs: []Need{NeedHostBackend}},
		{Path: "units audit", Summary: "audit VM process trees", BuiltBy: "M2-VM-TREE-AUDIT", Needs: []Need{NeedHostBackend}},
		{Path: "units gc", Summary: "collect failed and orphaned units", BuiltBy: "M3-CLUSTER-UNITS", Needs: []Need{NeedHostBackend}},
		{Path: "units roll", Summary: "roll kcp or a cluster's control plane", BuiltBy: "M3-KCP-ROLL", Needs: []Need{NeedHostBackend}},
		{Path: "internal devsup", Summary: "the per-instance proc backend supervisor", Needs: []Need{NeedProc}, Hidden: true},
		{Path: "internal dev-core", Summary: "the dev instance's core: adopts the bootstrap units and records them", Needs: []Need{NeedProc}, Hidden: true},
		{Path: "internal ns-exec", Summary: "private hostname one-shot for the proc backend", BuiltBy: "M3-CP-SLOTS", Hidden: true},
		{Path: "internal apiserver-front", Summary: "the L4 front of kcp or a tenant apiserver", BuiltBy: "M2-KCP-FRONT", Hidden: true},
		{Path: "internal cloud-controller-manager", Summary: "the tenant CCM", BuiltBy: "M3-FOLD", Hidden: true},
		{Path: "internal tenantcredential", Summary: "the tenant credential exec plugin", BuiltBy: "M3-FOLD", Hidden: true},
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
