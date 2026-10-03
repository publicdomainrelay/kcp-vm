package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

const usage = `socialweb-nodeimage - build the artifacts a node boots from

  socialweb-nodeimage -config nodeimage.json
  socialweb-nodeimage -config nodeimage.json -gc [-retain N] [-keep <fingerprint>]...

The image is named by its inputs, and the VMM is one of them. The configuration is fingerprinted --
its VMM, its module lists, the image references it seeds, the kernel, the kernel config, the base
image and the k3s binary, by path and by digest -- and the build is published under that name, as
<out_dir>/<fingerprint>/. Running this twice over one configuration builds once: the second run finds
the name already taken and exits without building, writing or pulling anything. Changing an input
gives a different name, so the image built before the change keeps its own name and is never asked
for again rather than being found and rejected. A firecracker image and a qemu image of the same
inputs do not collide, and each carries the VMM it was built for so a substrate refuses the other
one's image rather than booting it.

Every image is named by the tag the guest asks for AND the digest that tag resolved to, on both sides
of the '@', e.g. rancher/mirrored-coredns-coredns:1.14.6@sha256:1ba6f4726560... A bare tag is
refused, because a tag is not an identity: the registry it lives in can be pushed to at any time, and
when it is, the entry is unchanged, the name is unchanged, and the image the store already holds
under that name is not the one the tag now resolves to. The refusal resolves the tag and prints the
digest to write, so pinning an entry is a copy and paste. The entry has to carry the tag as well as
the digest, because the tag is what the guest's containerd matches a pod's request against: an image
seeded under a digest alone is one no pod ever asks for.

A build is written into a temporary directory beside the store and renamed into its name only once it
is whole, so a build killed by a crash, a full disk or a signal leaves a name nothing asks for
instead of a directory that is half an image and still validates.

The build is rootless and touches nothing outside its stage, its store and the image cache. It builds
the guest's half of the agent from this tree, extracts this host's own kernel, stages this host's own
module tree and kernel config beside that kernel, and writes three files into the image directory:

  vmlinux          the guest's kernel
  initramfs.cpio   the initramfs that loads the virtio transport and hands over to the root
  rootfs.ext4      the writable root filesystem, a TEMPLATE that every node is given a copy of

Nothing is ever booted from the template itself. Two guests sharing one writable ext4 corrupt it,
and one guest's k3s state read by the next guest's boot is a node that behaves differently from
the image it came from.

Nothing is overwritten, and nothing is removed by a build: a build that deleted the image a running
node booted from would do it as a side effect of being run. Removal is its own mode:

  -gc          reap, and nothing else. It keeps the image the configuration resolves to, every image
               named by -keep, and the newest -retain images. It removes the staging directories of
               builds that are no longer running -- a staging directory whose process is gone is a
               build that was killed, and one whose process is alive is a build in progress and is
               left alone however old it looks. It removes a directory that carries the name of an
               image and does not hold a whole one, which no build that ran to the end can leave.
               It leaves anything in the store that is not named like this store's own, and says so.
  -retain N    how many images the store keeps BESIDES the ones a name protects (default 3), which
               are the rollbacks a rebuild can be walked back to. Each is ~2.3 GB.
  -keep <fp>   an image to keep whatever -retain says, named by its fingerprint directory -- the
               name of its directory in the store. This is how the image a running node booted from
               is protected: nothing here can see which images a node is running from.
`

type keepFlags []string

func (k *keepFlags) String() string { return strings.Join(*k, ",") }

func (k *keepFlags) Set(value string) error {
	*k = append(*k, value)
	return nil
}

func main() {
	config := flag.String("config", "", "path to the image builder's config")
	gc := flag.Bool("gc", false, "reap: keep the configured image, the -keep images and the newest -retain images")
	retain := flag.Int("retain", retainDefault, "how many images -gc keeps in total")
	keep := keepFlags{}
	flag.Var(&keep, "keep", "a fingerprint directory -gc must keep; repeatable")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()
	run := Main
	if *gc {
		run = func(configPath string) error { return MainGC(configPath, keep, *retain) }
	}
	if err := run(*config); err != nil {
		fmt.Fprintf(os.Stderr, "socialweb-nodeimage: %v\n", err)
		os.Exit(1)
	}
}

func Main(configPath string) error {
	if configPath == "" {
		return fmt.Errorf("-config is required: the paths an image is built from are this host's " +
			"own kernel, module tree and base image, and none of them has a default that would be " +
			"right on another machine")
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	if err := buildAgent(cfg); err != nil {
		return err
	}
	fingerprint, err := nodefirecracker.Fingerprint(cfg.Path, buildConfigOf(cfg))
	if err != nil {
		return fmt.Errorf("cannot tell which image under %s the configuration at %s names, so there "+
			"is no name to ask for and nothing has been built or pulled: %w", cfg.OutDir, cfg.Path, err)
	}
	dir := storeDir(cfg, fingerprint)
	present, err := completeImage(cfg, fingerprint)
	if err != nil {
		return err
	}
	if present {
		fmt.Printf("up to date %s\n  config   %s\n  nothing built, nothing pulled\n", dir, cfg.Path)
		return nil
	}
	fmt.Printf("building %s\n  config   %s\n", dir, cfg.Path)
	artifacts, err := publishImage(cfg, fingerprint)
	if err != nil {
		return err
	}
	fmt.Printf("image %s\n  kernel    %s\n  initramfs %s\n  rootfs    %s\n",
		dir, artifacts.Kernel, artifacts.Initramfs, artifacts.Rootfs)
	return nil
}
