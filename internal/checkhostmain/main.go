package checkhostmain

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/user"
	"runtime"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/foldmain"
)

const usage = `socialweb-computer-kcp-check-host - the host contract this project's proofs are made of

  socialweb-computer-kcp-check-host [-config nodeimage.json]
  socialweb-computer-kcp-check-host -probe-exec

Every fact below is one this project has already paid for: a binary that had been relocated so the
live proofs looked impossible to run, a module list that was one name short and said so only on the
guest's own console, a block of ports that collided with a peer run and failed a readiness deadline
instead of a bind, and a guest whose network was a transcript of this host's LAN. None of them was
checked by anything, and all of them are host facts. This binary checks them and says which category
each one falls in:

  HOLDS              this environment carries it
  MISSING            the thing this environment is supposed to provide is not here, and this is
                     the only category that makes this exit non-zero
  MUST BE PASSED IN  a host fact this container has not been handed: a container has no kernel of
                     this host's, no module tree built beside it, no /dev/kvm, no permission to
                     create a user namespace, no VMM binary and no image built from that kernel

Which of MISSING and MUST BE PASSED IN a missing host fact is depends on where this runs, and the
header line says which: on a host the host is the thing that must provide it, in a container the
same fact is one the container has to be given.

Read this AS A BUILD STEP, not only as a probe run by hand. A stage that cannot check its own output
is a stage that ships an image nobody looked at: the kernel it baked, the module tree it staged and
the store it wrote are all facts about the machine the build ran on, and the same three lines that
tell an operator whether a host can provision a node tell a build whether the host it is running on
gave it what it needs. A build that runs this first and fails on MISSING fails before it writes 2.3
GB of root filesystem, and a build that runs it after writes its own verification into the log that
later says which host produced the image.

The paths come from the operator's config (default %s, or
-config, or %s) wherever it names them, and every one of them can be overridden:

  %s   the VMM binary          %s  the kernel to bake
  %s   the kernel's config     %s  the module tree to stage
  %s   the staging directory   %s  the image store
  %s   the k3s binary          %s  the kubectl used by tests
  %s   the kine binary         %s  the kcp binary
  %s   /dev/kvm                %s  how many images the store keeps
  %s   the tree the SELinux    %s  the postgres binaries
                     check builds in

Unset and unnamed, each falls back to a path derived from the release this host is running, which is
the honest default: a guest boots the host's own kernel, and the modules that can load in it are the
tree built beside that kernel. What has no honest default is reported as such rather than guessed.

-probe-exec makes this binary print one line and exit 0. It exists so the SELinux check can execute a
copy of this binary placed in the tree this project was checked out into: that is the shape that
failed once already, and it is checked by running, never by reading a label.

Exit status is 0 when nothing is MISSING, 1 when something is, and 2 when this binary could not read
the host well enough to check anything at all.
`

func Main(ctx context.Context, inv foldmain.Invocation) int {
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.SetOutput(inv.Stderr)
	config := fs.String("config", envOr(inv.Env, envConfigPath, defaultConfigPath),
		"the operator's nodeimage.json, which names the paths above")
	probeExec := fs.Bool("probe-exec", false,
		"print one line and exit 0, so this binary can be executed as a probe from a copied location")
	buildOnly := fs.Bool("build", false,
		"run as a build step: only the checks a build reads decide the exit status")
	fs.Usage = func() {
		fmt.Fprintf(inv.Stderr, usage, defaultConfigPath, envConfigPath,
			envFirecracker, envKernel, envKernelConfig, envModuleSource, envStageDir, envOutDir,
			envK3s, envKubectl, envKine, envKCP, envKVM, envRetain, envRepo, envPostgresBinDir)
	}
	if err := fs.Parse(inv.Args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *probeExec {
		fmt.Fprintln(inv.Stdout, "check-host exec probe: this binary ran from where it was placed")
		return 0
	}
	f, err := readFacts(*config, inv.Env)
	if err != nil {
		fmt.Fprintln(inv.Stderr, "MISSING            host.facts")
		for _, line := range wrap(err.Error(), 92) {
			fmt.Fprintf(inv.Stderr, "%-17s %s\n", "", line)
		}
		return 2
	}
	f.probeArgv = inv.Verb
	return writeReport(inv.Stdout, header(f), markBuildInputs(run(f)), *buildOnly)
}

func header(f facts) []string {
	who := fmt.Sprintf("uid %d gid %d", os.Geteuid(), os.Getegid())
	if current, err := user.Current(); err == nil && current.Username != "" {
		who += " (" + current.Username + ")"
	}
	if os.Geteuid() == 0 {
		who += ", which is ROOT"
	}
	configLine := f.configPath + " is readable"
	if len(f.configMissing) > 0 {
		configLine = f.configPath + " does not name: " + joinBounded(f.configMissing)
	}
	where := "this host, so a missing host fact is MISSING"
	if f.inContainer {
		where = "a container, so a host fact it has not been given is MUST BE PASSED IN"
	}
	return []string{
		fmt.Sprintf("host     %s on %s", f.release, runtime.GOARCH),
		fmt.Sprintf("user     %s", who),
		fmt.Sprintf("config   %s", configLine),
		fmt.Sprintf("repo     %s", f.repo),
		fmt.Sprintf("running  in %s", where),
	}
}

func joinBounded(names []string) string {
	if len(names) > 4 {
		return fmt.Sprintf("%d of the keys it is expected to carry", len(names))
	}
	out := ""
	for i, name := range names {
		if i > 0 {
			out += ", "
		}
		out += name
	}
	return out
}
