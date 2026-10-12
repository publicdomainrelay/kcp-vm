package kernelmodules

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var Extensions = []string{
	"DNAT",
	"MARK",
	"MASQUERADE",
	"NFLOG",
	"REJECT",
	"SNAT",
	"addrtype",
	"comment",
	"conntrack",
	"icmp",
	"ipvs",
	"limit",
	"mark",
	"multiport",
	"physdev",
	"set",
	"statistic",
	"tcp",
	"udp",
}

type Tree struct {
	files    map[string]struct{}
	builtins map[string]struct{}
	aliases  map[string]string
}

func LoadTree(source string) (Tree, error) {
	tree := Tree{
		files:    make(map[string]struct{}),
		builtins: make(map[string]struct{}),
		aliases:  make(map[string]string),
	}
	deps, err := os.ReadFile(filepath.Join(source, "modules.dep"))
	if err != nil {
		return tree, fmt.Errorf("kernelmodules: reading the module index under %s: %w -- the "+
			"index is what decides which of the extensions kube-proxy, kubelet and the cluster's "+
			"network policy controller reach for are even present on this kernel, and a read that "+
			"walks the module files instead of this index calls a name present that modprobe "+
			"cannot resolve", source, err)
	}
	for _, line := range strings.Split(string(deps), "\n") {
		path, _, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		if name := moduleBaseName(path); name != "" {
			tree.files[name] = struct{}{}
		}
	}
	builtins, err := os.ReadFile(filepath.Join(source, "modules.builtin"))
	if err != nil {
		return tree, fmt.Errorf("kernelmodules: reading %s: %w -- a kernel built with an "+
			"extension compiled in carries no module file for it, so without this list a name that "+
			"is already in the running kernel would be refused as missing",
			filepath.Join(source, "modules.builtin"), err)
	}
	for _, line := range strings.Split(string(builtins), "\n") {
		if name := moduleBaseName(line); name != "" {
			tree.builtins[name] = struct{}{}
		}
	}
	aliases, err := os.ReadFile(filepath.Join(source, "modules.alias"))
	if err != nil {
		return tree, fmt.Errorf("kernelmodules: reading %s: %w -- several iptables "+
			"extensions are provided by a module whose file name is not their name, and the alias "+
			"table is the only place that mapping is written down by the kernel build itself",
			filepath.Join(source, "modules.alias"), err)
	}
	for _, line := range strings.Split(string(aliases), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != "alias" {
			continue
		}
		tree.aliases[fields[1]] = fields[2]
	}
	return tree, nil
}

func moduleBaseName(path string) string {
	name := filepath.Base(strings.TrimSpace(path))
	if name == "." || name == "/" || name == "" {
		return ""
	}
	for _, suffix := range []string{".xz", ".zst", ".gz", ".ko"} {
		name = strings.TrimSuffix(name, suffix)
	}
	return name
}

func (t Tree) Resolves(name string) bool {
	if _, ok := t.files[name]; ok {
		return true
	}
	if _, ok := t.builtins[name]; ok {
		return true
	}
	if _, ok := t.aliases[name]; ok {
		return true
	}
	return false
}

func (t Tree) Providers(extension string) []string {
	seen := make(map[string]struct{})
	for _, prefix := range []string{"xt_", "ipt_", "ip6t_"} {
		request := prefix + extension
		if _, ok := t.files[request]; ok {
			seen[request] = struct{}{}
		}
		if name, ok := t.aliases[request]; ok {
			seen[name] = struct{}{}
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (t Tree) Files() int { return len(t.files) }

func (t Tree) Builtins() int { return len(t.builtins) }

func (t Tree) Aliases() int { return len(t.aliases) }

func Verify(source string, modules []string) error {
	tree, err := LoadTree(source)
	if err != nil {
		return err
	}
	named := make(map[string]struct{}, len(modules))
	for _, name := range modules {
		if !tree.Resolves(name) {
			return fmt.Errorf("kernelmodules: modules names %s, and the kernel this tree belongs "+
				"to carries no module by that name: it is in neither modules.dep, nor "+
				"modules.builtin, nor modules.alias under %s. A name the module tree does not carry "+
				"is a modprobe that fails inside the guest, where the only trace of it is a "+
				"KCP_MOD_FAIL line on a console nobody reads until a Service has no address, and "+
				"the iptables extension such a name was meant to satisfy is not the name of the "+
				"module that satisfies it: the target REJECT is provided by ipt_REJECT and "+
				"ip6t_REJECT, the targets DNAT and SNAT by xt_nat, and the request the kernel makes "+
				"for an extension no module file is named after resolves to nothing at all",
				name, source)
		}
		named[name] = struct{}{}
	}
	var missing []string
	for _, extension := range Extensions {
		providers := tree.Providers(extension)
		if len(providers) == 0 {
			continue
		}
		satisfied := false
		for _, provider := range providers {
			if _, ok := tree.builtins[provider]; ok {
				satisfied = true
				break
			}
			if _, ok := named[provider]; ok {
				satisfied = true
				break
			}
		}
		if !satisfied {
			missing = append(missing, fmt.Sprintf("%s (provided by %s)",
				extension, strings.Join(providers, " or ")))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("kernelmodules: the guest booting this tree reaches for "+
			"iptables extensions that nothing on its boot path loads, and iptables inside such a "+
			"guest does not load them on demand: %s. iptables-restore stops at the FIRST extension "+
			"it cannot find, so a guest missing one of these comes up with the rules that precede "+
			"the first missing extension applied and every rule after it absent, and it says so "+
			"only on its own console: kube-proxy logs a failed iptables-restore and leaves "+
			"10.43.0.1:443 without an address, and the network policy controller logs an aborted "+
			"sync and leaves the cluster's pods with no firewall at all. Name every module listed "+
			"above in modules", strings.Join(missing, "; "))
	}
	return nil
}
