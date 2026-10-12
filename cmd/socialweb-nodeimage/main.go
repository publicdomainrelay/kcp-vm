package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

const usage = `socialweb-nodeimage - build the artifacts a node boots from

  socialweb-nodeimage -config nodeimage.json [-init-asset socialweb-nodeinit]
  socialweb-nodeimage -ensure -config nodeimage.json [-reuse-stale] [-json] [-init-asset socialweb-nodeinit]
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
the guest's init from this tree, extracts this host's own kernel, stages this host's own
module tree and kernel config beside that kernel, and writes three files into the image directory:

  vmlinux          the guest's kernel
  initramfs.cpio   the initramfs that loads the virtio transport and hands over to the root
  rootfs.ext4      the writable root filesystem, a TEMPLATE that every node is given a copy of

Nothing is ever booted from the template itself. Two guests sharing one writable ext4 corrupt it,
and one guest's k3s state read by the next guest's boot is a node that behaves differently from
the image it came from.

Nothing is overwritten, and nothing is removed by a build: a build that deleted the image a running
node booted from would do it as a side effect of being run. Removal is its own mode:

  -ensure      make the image the configuration resolves to available, and nothing else. When the
               store already holds it whole under its own name, this builds nothing, pulls nothing and
               writes nothing, which is what makes it safe to run at the start of anything that boots
               a guest: a host that has been asleep through a kernel or base image change brings the
               image up to date on the way up instead of failing at the first placement. Where -config
               alone refuses, this builds; where a build is not wanted, see -reuse-stale.
  -reuse-stale with -ensure: do not build. Use the newest image already in the store that is whole
               AND built for the VMM this configuration places with, and say which name it came from.
               The name the inputs give is still computed and still reported, so a caller can see the
               distance between what it asked for and what it got. If the store holds no such image
               this refuses and builds nothing: the images in a store are named by the inputs that
               built them, so the newest one there is an image built from inputs this configuration
               does not describe, and booting it would be booting the image from before the change.
               This is the switch for a host that must come up without a build, not a fallback.
  -present-only with -ensure: answer whether the store already holds the exact image these inputs
               name, and answer it without building, pulling, reusing or writing anything: the state
               is "present" and the exit is 0 when it does, and the state is "absent" with a non-zero
               exit when it does not. This is the question a build host has to ask before it decides
               to build, because -ensure alone answers the same question by BUILDING: on a host whose
               user cannot mount, that build gets as far as the preinstall chroot -- after the base
               image and every seeded image have been unpacked into the stage -- and then fails, so
               asking with -ensure costs a full stage and answers nothing. -reuse-stale is not this
               answer either: it names an image that is by construction not the one these inputs
               name. It is a question, so it never changes the store, and a caller that reads it as
               "build it if it is absent" owns that build itself.
  -json        with -ensure: print one JSON object, so a caller that is not a person can read the
               state, the name, the directory and, when one was reused, the name it came from. The
               object is the LAST line of standard output: a build reports each artifact as it is
               written, and those lines stay, because a build can take minutes and one that says
               nothing until it is finished is indistinguishable from one that has hung. A caller
               reads the last line that parses, not the only line.
  -preinstall <path>
               a manifest naming software to bake into the image, overriding whatever the
               configuration names. The image is named by its inputs and the manifest is one of
               them, by digest, so changing what is baked in changes the name: without that, a
               package added to the list would keep the old name and every guest would boot the
               software from before the change. This exists as a flag rather than only as a
               configuration key so that a caller which derives the list can pass it without
               writing a second configuration file -- the name records the configuration's path,
               so a generated file under a temporary directory would be an image rebuilt from
               scratch every time that directory changed.
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
	ensure := flag.Bool("ensure", false, "make the image the config names available, building it only when it is not already there")
	reuseStale := flag.Bool("reuse-stale", false, "with -ensure: use an image already in the store instead of building the one the config names")
	asJSON := flag.Bool("json", false, "with -ensure: print one line of JSON describing what was found, built or reused")
	preinstall := flag.String("preinstall", "", "path to a preinstall manifest, overriding the one the config names")
	initAsset := flag.String("init-asset", "", "path to a prebuilt socialweb-nodeinit the image carries as its init, overriding the config's init_asset")
	presentOnly := flag.Bool("present-only", false, "with -ensure: answer whether the store already holds the exact image the config names, and build nothing")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()
	run := func(configPath string) error { return Main(configPath, *initAsset) }
	switch {
	case *presentOnly && !*ensure:
		run = func(string) error {
			return fmt.Errorf("-present-only asks a question about -ensure -- whether the store " +
				"already holds the exact image these inputs name -- and there is no -ensure here for " +
				"it to ask it of")
		}
	case *presentOnly && *reuseStale:
		run = func(string) error {
			return fmt.Errorf("-present-only asks whether the store holds the exact image these " +
				"inputs name, and -reuse-stale answers that question with the newest whole image in " +
				"the store whatever its inputs: the two cannot both answer it, and the answer that " +
				"matters is the one that says whether the inputs in front of this run have been " +
				"built, so one of them has to go")
		}
	case *gc:
		run = func(configPath string) error { return MainGC(configPath, keep, *retain) }
	case *ensure:
		run = func(configPath string) error {
			return Ensure(configPath, *reuseStale, *asJSON, *preinstall, *presentOnly, *initAsset)
		}
	case *preinstall != "":
		run = func(string) error {
			return fmt.Errorf("-preinstall says which software to bake into the image, and the " +
				"image it would be baked into is one only -ensure asks for: run -ensure, which " +
				"builds when the store does not already hold the image these inputs name")
		}
	case *reuseStale || *asJSON:
		run = func(string) error {
			return fmt.Errorf("-reuse-stale and -json describe what -ensure did, and neither means " +
				"anything without it: -reuse-stale is answered by the store rather than by a build, " +
				"and -json is the shape of that answer")
		}
	}
	if err := run(*config); err != nil {
		fmt.Fprintf(os.Stderr, "socialweb-nodeimage: %v\n", err)
		os.Exit(1)
	}
}

func Main(configPath string, initAsset string) error {
	if configPath == "" {
		return fmt.Errorf("-config is required: the paths an image is built from are this host's " +
			"own kernel, module tree and base image, and none of them has a default that would be " +
			"right on another machine")
	}
	cfg, err := LoadConfig(configPath)
	if err != nil {
		return err
	}
	if err := cfg.overrideInitAsset(initAsset); err != nil {
		return err
	}
	return withStageDirHeld(cfg, func() error {
		return buildImage(cfg)
	})
}

func buildImage(cfg Config) error {
	if err := buildInit(cfg); err != nil {
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
