//go:build linux

package nodefirecracker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEveryInputTheComparisonWeighsIsAnInputTheNameIsMadeOf(t *testing.T) {
	for _, change := range []struct {
		name  string
		apply func(t *testing.T, f imageFixture) BuildConfig
	}{
		{"the kernel module list", func(t *testing.T, f imageFixture) BuildConfig {
			f.cfg.Modules = append(append([]string(nil), f.cfg.Modules...), "xt_NFLOG")
			return f.cfg
		}},
		{"the initramfs module list", func(t *testing.T, f imageFixture) BuildConfig {
			f.cfg.InitramfsModules = append(append([]string(nil), f.cfg.InitramfsModules...),
				"kernel/drivers/virtio/virtio_pci")
			return f.cfg
		}},
		{"the seeded image list", func(t *testing.T, f imageFixture) BuildConfig {
			f.cfg.Images = append(append([]string(nil), f.cfg.Images...),
				"rancher/mirrored-coredns-coredns:1.14.6")
			return f.cfg
		}},
		{"the path of the kernel", func(t *testing.T, f imageFixture) BuildConfig {
			other := filepath.Join(f.root, "vmlinuz-6.17.0")
			writeFileAt(t, other, "a kernel no build has read\n")
			f.cfg.KernelSource = other
			return f.cfg
		}},
		{"the contents of the kernel", func(t *testing.T, f imageFixture) BuildConfig {
			writeFileAt(t, f.kernel, "this host's kernel, after an upgrade replaced it in place\n")
			return f.cfg
		}},
		{"the contents of the kernel configuration", func(t *testing.T, f imageFixture) BuildConfig {
			writeFileAt(t, f.kernelEnv, "CONFIG_NETFILTER_XT_TARGET_NFLOG=y\n")
			return f.cfg
		}},
		{"the contents of the base image", func(t *testing.T, f imageFixture) BuildConfig {
			writeFileAt(t, f.baseImage, "a base userspace, rebuilt in place since the last build\n")
			return f.cfg
		}},
		{"the path of the k3s binary", func(t *testing.T, f imageFixture) BuildConfig {
			other := filepath.Join(f.root, "k3s-1.32")
			writeFileAt(t, other, "k3s version v1.32.0+k3s1\n")
			f.cfg.K3sBinary = other
			return f.cfg
		}},
		{"the contents of the k3s binary", func(t *testing.T, f imageFixture) BuildConfig {
			writeFileAt(t, f.k3s, "k3s version v1.32.0+k3s1\n")
			return f.cfg
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			f := newImageFixture(t)
			manifest := f.manifest(t, f.cfg)
			before, err := Fingerprint(f.configPath, f.cfg)
			if err != nil {
				t.Fatalf("naming the inputs a build from this configuration reads: %v", err)
			}
			changed := change.apply(t, f)
			f.writeConfig(t, changed)

			if err := manifest.CheckInputs(); err == nil {
				t.Errorf("the comparison weighs %s and a configuration that changed it was accepted, "+
					"so either the comparison has lost an input or this test is wrong about which "+
					"inputs it weighs", change.name)
			}
			after, err := Fingerprint(f.configPath, changed)
			if err != nil {
				t.Fatalf("naming the inputs after %s changed: %v", change.name, err)
			}
			if after == before {
				t.Errorf("%s is an input the comparison weighs and the name ignores, so a "+
					"configuration that changed it names the image that was built BEFORE the change: "+
					"that image is consumed with nothing to reject it, and the guest it boots is "+
					"missing whatever the change added, which is the one failure this whole check "+
					"exists to make impossible", change.name)
			}
		})
	}
}

func TestTheNameCoversWhichConfigurationTheBuildRead(t *testing.T) {
	f := newImageFixture(t)
	here, err := Fingerprint(f.configPath, f.cfg)
	if err != nil {
		t.Fatalf("naming the inputs: %v", err)
	}
	there, err := Fingerprint(filepath.Join(f.root, "nodeimage-elsewhere.json"), f.cfg)
	if err != nil {
		t.Fatalf("naming the inputs under another configuration path: %v", err)
	}
	if here == there {
		t.Fatal("the same inputs reached through another configuration path name the same image, and " +
			"the image records the path the build read: a substrate that read that record back would " +
			"load the file the build read rather than the one in front of it, and compare the image " +
			"against a configuration nobody wrote")
	}
}

func TestTheNameIsTheSameHoweverTheConfigurationIsSpelled(t *testing.T) {
	f := newImageFixture(t)
	absolute, err := Fingerprint(f.configPath, f.cfg)
	if err != nil {
		t.Fatalf("naming the inputs: %v", err)
	}
	t.Chdir(f.root)
	relative, err := Fingerprint(filepath.Base(f.configPath), f.cfg)
	if err != nil {
		t.Fatalf("naming the inputs from the directory the configuration lives in: %v", err)
	}
	if relative != absolute {
		t.Fatal("one configuration reached by two spellings of its path names two images, and " +
			"whoever wrote the second spelling asked the store for one that is not there: a store " +
			"that fills with 2.3 GB copies of one image, and a rebuild that was never due")
	}
}

func TestOneConfigurationReachedThroughASymlinkNamesOneImage(t *testing.T) {
	f := newImageFixture(t)
	direct, err := Fingerprint(f.configPath, f.cfg)
	if err != nil {
		t.Fatalf("naming the inputs: %v", err)
	}
	link := filepath.Join(f.root, "the-config-in-its-place")
	if err := os.Symlink(f.root, link); err != nil {
		t.Fatalf("linking %s to %s: %v", link, f.root, err)
	}
	throughLink, err := Fingerprint(filepath.Join(link, filepath.Base(f.configPath)), f.cfg)
	if err != nil {
		t.Fatalf("naming the inputs through %s: %v", link, err)
	}
	if throughLink != direct {
		t.Fatal("one configuration reached directly and reached through a symlink names two images. " +
			"An operator who keeps the configuration behind a symlink -- which is what a config " +
			"managed as a symlink into a checkout is -- asks the store for a name that is not there, " +
			"so the build runs again over the same inputs, publishes 2.3 GB beside the image it " +
			"already has, and nothing says the two are the same image")
	}
}

func TestAnInputReachedThroughASymlinkNamesTheSameImageAsTheFileItself(t *testing.T) {
	f := newImageFixture(t)
	direct, err := Fingerprint(f.configPath, f.cfg)
	if err != nil {
		t.Fatalf("naming the inputs: %v", err)
	}
	linked := make(map[string]string, 4)
	for _, path := range []string{f.kernel, f.kernelEnv, f.baseImage, f.k3s} {
		alias := path + ".current"
		if err := os.Symlink(path, alias); err != nil {
			t.Fatalf("linking %s to %s: %v", alias, path, err)
		}
		linked[path] = alias
	}
	viaLinks := f.cfg
	viaLinks.KernelSource = linked[f.kernel]
	viaLinks.KernelConfig = linked[f.kernelEnv]
	viaLinks.BaseImage = linked[f.baseImage]
	viaLinks.K3sBinary = linked[f.k3s]

	throughLinks, err := Fingerprint(f.configPath, viaLinks)
	if err != nil {
		t.Fatalf("naming the inputs through the symlinks: %v", err)
	}
	if throughLinks != direct {
		t.Fatal("the same four files reached through symlinks name a second image. Every one of " +
			"these paths is a file an operator reaches the way their distribution names it -- " +
			"/lib/modules beside /usr/lib/modules, a kernel behind a version symlink -- and a name " +
			"that follows the spelling gives that file two images: a second 2.3 GB build of inputs " +
			"that did not change, and a store in which neither image can be shown to be the other")
	}
}

func TestAPathThatCannotBeResolvedIsNotAnInputThatHasNotChanged(t *testing.T) {
	f := newImageFixture(t)
	gone := filepath.Join(f.root, "a-directory-that-is-not-there")
	for _, c := range []struct {
		name  string
		path  string
		apply func(BuildConfig) BuildConfig
	}{
		{"the configuration", filepath.Join(gone, "nodeimage.json"), func(cfg BuildConfig) BuildConfig { return cfg }},
		{"the kernel", filepath.Join(gone, "vmlinuz"), func(cfg BuildConfig) BuildConfig {
			cfg.KernelSource = filepath.Join(gone, "vmlinuz")
			return cfg
		}},
		{"the base image", filepath.Join(gone, "base.squashfs"), func(cfg BuildConfig) BuildConfig {
			cfg.BaseImage = filepath.Join(gone, "base.squashfs")
			return cfg
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			configPath := f.configPath
			if c.name == "the configuration" {
				configPath = c.path
			}
			_, err := Fingerprint(configPath, c.apply(f.cfg))
			if err == nil {
				t.Fatalf("a build whose %s is at %s, which cannot be resolved, was named. An input "+
					"that cannot be resolved cannot be shown to be the file a previous build read, "+
					"and this is the one case where a stale image and a current one are "+
					"indistinguishable: a name handed out here is the store answering 'the image "+
					"you asked for is the one you have' on the strength of a path it never read",
					c.name, c.path)
			}
			if !strings.Contains(err.Error(), c.path) {
				t.Fatalf("the refusal does not name the path it could not resolve: %v", err)
			}
		})
	}
}

func TestTheNameIsTheSameForTheSameInputsInAnotherOrder(t *testing.T) {
	f := newImageFixture(t)
	before, err := Fingerprint(f.configPath, f.cfg)
	if err != nil {
		t.Fatalf("naming the inputs: %v", err)
	}
	again, err := Fingerprint(f.configPath, f.cfg)
	if err != nil {
		t.Fatalf("naming the inputs a second time: %v", err)
	}
	if again != before {
		t.Fatal("two runs over one configuration named it two ways, and the second run would build " +
			"a second image of 2.3 GB for a store that already held the first")
	}
	reordered := f.cfg
	reordered.Modules = []string{"xt_nat", "xt_MASQUERADE", "nf_conntrack", "xt_nat"}
	after, err := Fingerprint(f.configPath, reordered)
	if err != nil {
		t.Fatalf("naming the inputs with the module list in another order: %v", err)
	}
	if after != before {
		t.Fatal("the module list is a SET -- the comparison in this package treats it as one and " +
			"accepts the same modules in any order -- so a configuration that lists them in another " +
			"order, or lists one twice, names an image that does not exist: the store fills with " +
			"2.3 GB copies of one image and a guest is told to rebuild for nothing")
	}
}

func builderDigestOf(digest string) func() (string, error) {
	return func() (string, error) { return digest, nil }
}

func nameFrom(t *testing.T, f imageFixture, digest string) string {
	t.Helper()
	inputs, err := recordInputs(f.configPath, f.cfg, builderDigestOf(digest))
	if err != nil {
		t.Fatalf("recording the inputs read by the builder whose own digest is %s: %v", digest, err)
	}
	name, err := inputs.Fingerprint()
	if err != nil {
		t.Fatalf("naming the inputs read by the builder whose own digest is %s: %v", digest, err)
	}
	return name
}

func TestABuilderThatChangedNamesAnotherImageOverOneConfiguration(t *testing.T) {
	f := newImageFixture(t)
	before := nameFrom(t, f, "sha256:"+strings.Repeat("a", 64))
	again := nameFrom(t, f, "sha256:"+strings.Repeat("a", 64))
	if before != again {
		t.Fatalf("one builder named one configuration two ways (%s and %s), so a store fills with "+
			"2.3 GB copies of one image and the build that was never due runs again", before, again)
	}
	after := nameFrom(t, f, "sha256:"+strings.Repeat("b", 64))
	if after == before {
		t.Fatalf("two builders name one configuration %s. Everything else about the two is equal -- "+
			"the configuration file, the kernel, the module tree, the base image, the k3s binary, the "+
			"init -- so the builder itself is the only input that changed and the name is the only "+
			"place anything downstream would see it: the store answers 'this image is the one your "+
			"inputs name' with the image the earlier builder wrote, the installer ships it, and the "+
			"guest runs the behaviour from before the builder changed while nothing in the log says so",
			before)
	}
}

func TestTheBuilderTheRecordNamesIsTheBinaryThatReadTheConfiguration(t *testing.T) {
	f := newImageFixture(t)
	inputs, err := RecordInputs(f.configPath, f.cfg)
	if err != nil {
		t.Fatalf("recording the inputs: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("finding the binary this test is running as: %v", err)
	}
	want := independentDigestOf(t, self)
	if inputs.BuilderDigest != want {
		t.Fatalf("the record of a build's inputs carries the builder digest %q and the binary reading "+
			"the configuration is %s, which digests to %s. The digest has to be of the process that "+
			"does the work: a digest of anything else -- a path recorded in the configuration, a "+
			"constant, an earlier build of the builder -- is a name that stops moving exactly when a "+
			"rebuild of the builder is what changed", inputs.BuilderDigest, self, want)
	}
	name, err := inputs.Fingerprint()
	if err != nil {
		t.Fatalf("naming the inputs: %v", err)
	}
	if !IsFingerprint(name) {
		t.Fatalf("the name given to an image of these inputs is %q, which is not a name this store "+
			"issues", name)
	}
}

func TestABuilderThatCannotBeReadIsNotAnInputThatHasNotChanged(t *testing.T) {
	f := newImageFixture(t)
	_, err := recordInputs(f.configPath, f.cfg, func() (string, error) {
		return "", errors.New("the builder binary is not there")
	})
	if err == nil {
		t.Fatal("a build whose own builder binary cannot be read was named. A digest that cannot be " +
			"taken cannot be shown to be the one a previous build recorded, and this is the same hole " +
			"as an unresolvable input path: the store answers with the image from before the builder " +
			"changed and nothing downstream can tell")
	}
	if !strings.Contains(err.Error(), "the builder binary is not there") {
		t.Fatalf("the refusal does not carry the reason the builder could not be read: %v", err)
	}
}

func independentDigestOf(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		t.Fatalf("digesting %s: %v", path, err)
	}
	return "sha256:" + hex.EncodeToString(sum.Sum(nil))
}
