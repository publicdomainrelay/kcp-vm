package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
)

// ponytail: the guest agent is built once and copied per fixture; building it
// inside every validConfig call cost ~25s of the fast loop. Build the fixture
// afresh if a test ever needs to mutate the toolchain inputs.
var (
	agentBuildOnce sync.Once
	agentBuildBin  string
	agentBuildErr  error
)

func cachedAgent() (string, error) {
	agentBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "kcp-nodeagent-")
		if err != nil {
			agentBuildErr = err
			return
		}
		bin := filepath.Join(dir, "kcpagent")
		agentBuildErr = buildAgent(Config{AgentPackage: "./cmd/socialweb-nodeagent", AgentBinary: bin})
		agentBuildBin = bin
	})
	return agentBuildBin, agentBuildErr
}

func copyCachedAgent(dst string) error {
	src, err := cachedAgent()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}

func validConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	agent := filepath.Join(root, "kcpagent")
	modules := filepath.Join(root, "modules")
	if err := os.MkdirAll(filepath.Join(modules, "kernel/net/netfilter"), 0o755); err != nil {
		t.Fatalf("creating the module tree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(modules, "kernel/drivers/virtio"), 0o755); err != nil {
		t.Fatalf("creating the module tree: %v", err)
	}
	for name, body := range map[string]string{
		"modules.dep": "kernel/net/netfilter/nf_conntrack.ko:\n" +
			"kernel/drivers/virtio/virtio_mmio.ko:\n",
		"modules.builtin":                      "kernel/net/netfilter/xt_NFLOG.ko\n",
		"modules.alias":                        "",
		"kernel/net/netfilter/nf_conntrack.ko": "the connection tracker\n",
		"kernel/drivers/virtio/virtio_mmio.ko": "the firecracker transport\n",
	} {
		if err := os.WriteFile(filepath.Join(modules, name), []byte(body), 0o600); err != nil {
			t.Fatalf("staging the module tree's %s: %v", name, err)
		}
	}
	cfg := Config{
		Firecracker:      "/usr/bin/firecracker",
		KernelSource:     "/boot/vmlinuz",
		KernelConfig:     "/boot/config",
		ModuleSource:     modules,
		BaseImage:        "/img/base.squashfs",
		StageDir:         filepath.Join(root, "stage"),
		OutDir:           filepath.Join(root, "out"),
		ImageCacheDir:    filepath.Join(root, "cache"),
		AgentPackage:     "./cmd/socialweb-nodeagent",
		AgentBinary:      agent,
		K3sBinary:        "/usr/local/bin/k3s",
		K3sDataDir:       "/var/lib/rancher/k3s",
		Images:           []string{"rancher/mirrored-pause:3.10.2"},
		BootArgs:         []string{"console=ttyS0"},
		Modules:          []string{"nf_conntrack"},
		InitramfsModules: []string{"kernel/drivers/virtio/virtio_mmio"},
	}
	if err := copyCachedAgent(agent); err != nil {
		t.Fatalf("building the guest's agent for this fixture: %v", err)
	}
	return cfg
}

const (
	pinnedCoreDNS = "rancher/mirrored-coredns-coredns:1.14.6@" +
		"sha256:1ba6f47265602e2e50a9c4669e3a955e4298a0d30dc82f39293d4bf1a851e0ff"

	pinnedCoreDNSDigest = "sha256:1ba6f47265602e2e50a9c4669e3a955e4298a0d30dc82f39293d4bf1a851e0ff"
)

func TestAnImageEntryIsATagAndTheDigestThatTagResolvedTo(t *testing.T) {
	one, err := parseImageReference(pinnedCoreDNS)
	if err != nil {
		t.Fatalf("parsing %s: %v", pinnedCoreDNS, err)
	}
	if !one.pinned() {
		t.Fatalf("%s was not taken as an entry that names its image by tag and by digest, and the "+
			"whole point of the entry is that both are inputs the image's name is made of", pinnedCoreDNS)
	}
	if one.seededAs != "rancher/mirrored-coredns-coredns:1.14.6" {
		t.Fatalf("the tag this entry seeds the guest under is %q. The guest's containerd matches "+
			"what a pod asks for against the tag, so seeding it under anything else is an image "+
			"nothing ever asks for", one.seededAs)
	}
	if one.digest.DigestStr() != pinnedCoreDNSDigest {
		t.Fatalf("the digest this entry carries is %q and it is %q in the entry",
			one.digest.DigestStr(), pinnedCoreDNSDigest)
	}
	if one.text != pinnedCoreDNS {
		t.Fatalf("the entry is recorded as %q, and the manifest has to carry the entry as it was "+
			"written: the substrate compares that record against the configuration in front of it "+
			"with no registry and no image at hand", one.text)
	}
}

func TestABareTagIsRefusedNamingTheDigestItShouldBePinnedTo(t *testing.T) {
	tag := "rancher/mirrored-coredns-coredns:1.14.6"
	one, err := parseImageReference(tag)
	if err != nil {
		t.Fatalf("parsing %s: %v", tag, err)
	}
	if one.pinned() {
		t.Fatalf("%s was accepted as pinned, and it carries no digest at all", tag)
	}
	digest := v1.Hash{Algorithm: "sha256",
		Hex: strings.TrimPrefix(pinnedCoreDNSDigest, "sha256:")}
	err = unpinnedTagRefusal(one, digest)
	if err == nil {
		t.Fatal("an entry that names its image by tag was accepted")
	}
	for _, want := range []string{tag, digest.String(), pinnedCoreDNS} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q. A refusal that does not hand the operator the "+
				"exact entry to write leaves them resolving a digest by hand, and the entry this "+
				"one asks for is the one the tag resolved to at the moment of the refusal: %v",
				want, err)
		}
	}
}

func TestAnEntryPinnedToADigestWithNoTagIsRefusedBecauseNothingAsksForIt(t *testing.T) {
	repository := "rancher/mirrored-coredns-coredns@" + pinnedCoreDNSDigest
	one, err := parseImageReference(repository)
	if err != nil {
		t.Fatalf("parsing %s: %v", repository, err)
	}
	if one.pinned() {
		t.Fatalf("%s was accepted as pinned. It carries a digest and no tag, and the tag is what "+
			"the guest's containerd matches a pod's request against, so an image seeded under a "+
			"digest alone is one no pod ever asks for and no pod ever gets", repository)
	}
	err = refuseUnpinned(one)
	if err == nil {
		t.Fatal("an entry with a digest and no tag was accepted")
	}
	for _, want := range []string{repository, pinnedCoreDNSDigest, "rancher/mirrored-coredns-coredns"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q, so it does not say what to write instead: %v",
				want, err)
		}
	}
}

func TestABuildRefusesAnEntryThatIsNotPinnedBeforeItPullsAnything(t *testing.T) {
	cfg := validConfig(t)
	cfg.Images = []string{"rancher/mirrored-pause@sha256:" + strings.Repeat("a", 64)}
	err := checkImagePins(cfg)
	if err == nil {
		t.Fatal("a build over an entry that names its image by digest alone went ahead. The tag is " +
			"what the guest's containerd holds, so this entry produces an image no pod asks for, " +
			"and the refusal has to come before the build pulls 2.3 GB of it")
	}
	if !strings.Contains(err.Error(), "rancher/mirrored-pause") {
		t.Fatalf("the refusal does not name the entry it is about: %v", err)
	}

	cfg.Images = []string{"rancher/mirrored-pause:3.10.2", "::not a reference::"}
	if err := checkImagePins(cfg); err == nil {
		t.Fatal("a build over an entry that is not a container image reference went ahead")
	}

	pinned := validConfig(t)
	pinned.Images = []string{pinnedCoreDNS}
	if err := checkImagePins(pinned); err != nil {
		t.Fatalf("a build over an entry that names its image by tag and by digest was refused: %v", err)
	}
}

func TestAnEntryThatNamesADigestWithNoRepositoryIsRefused(t *testing.T) {
	for _, entry := range []string{
		"rancher/mirrored-pause@sha256:notadigest",
		"@sha256:" + strings.Repeat("a", 64),
		"rancher/mirrored-pause:3.10.2@",
	} {
		if _, err := parseImageReference(entry); err == nil {
			t.Fatalf("%q was accepted as an image reference", entry)
		}
	}
}

func TestTwoEntriesUnderOneTagAreRefusedBecauseTheGuestHoldsOneImagePerTag(t *testing.T) {
	cfg := validConfig(t)
	cfg.Images = []string{
		"rancher/mirrored-coredns-coredns:1.14.6@sha256:" + strings.Repeat("a", 64),
		"index.docker.io/rancher/mirrored-coredns-coredns:1.14.6@sha256:" + strings.Repeat("b", 64),
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("two entries naming one tag were accepted. Both tarballs are seeded under that " +
			"tag, containerd keeps one image per tag, and which of the two a pod gets depends on " +
			"the order the tarballs are imported")
	}
	if !strings.Contains(err.Error(), "one name") {
		t.Fatalf("the refusal does not say that the two entries collide on one name: %v", err)
	}
}

func TestAnImageListThatIsEmptyIsRefusedBecauseTheGuestCannotPull(t *testing.T) {
	cfg := validConfig(t)
	cfg.Images = nil
	err := cfg.Validate()
	if err == nil {
		t.Fatal("an image builder with no images to seed accepted its config")
	}
	if !strings.Contains(err.Error(), "images is empty") {
		t.Fatalf("the refusal does not name the empty list: %v", err)
	}
}

func TestTheImageListRefusesBlankAndRepeatedEntries(t *testing.T) {
	for _, images := range [][]string{
		{"rancher/mirrored-pause:3.10.2", "  "},
		{"rancher/mirrored-pause:3.10.2", "rancher/mirrored-pause:3.10.2"},
	} {
		cfg := validConfig(t)
		cfg.Images = images
		if err := cfg.Validate(); err == nil {
			t.Fatalf("%q was accepted as an image list", images)
		}
	}
}

func TestTheImageCacheRefusesToLiveInsideTheImage(t *testing.T) {
	root := t.TempDir()
	for _, cache := range []string{
		filepath.Join(root, "out", "cache"),
		filepath.Join(root, "stage", "cache"),
	} {
		cfg := validConfig(t)
		cfg.StageDir = filepath.Join(root, "stage")
		cfg.OutDir = filepath.Join(root, "out")
		cfg.ImageCacheDir = cache
		err := cfg.Validate()
		if err == nil {
			t.Fatalf("a cache at %s, inside the image, was accepted", cache)
		}
		if !strings.Contains(err.Error(), "image_cache_dir") {
			t.Fatalf("the refusal does not name image_cache_dir: %v", err)
		}
	}
}

func TestTheImageCacheMustBeNamed(t *testing.T) {
	cfg := validConfig(t)
	cfg.ImageCacheDir = ""
	err := cfg.Validate()
	if err == nil {
		t.Fatal("a config with no image_cache_dir was accepted")
	}
	if !strings.Contains(err.Error(), "image_cache_dir") {
		t.Fatalf("the refusal does not name image_cache_dir: %v", err)
	}
}

func TestEveryDockerHubSpellingOfAReferenceIsSeededUnderOneName(t *testing.T) {
	for _, spelling := range []string{
		"rancher/mirrored-coredns-coredns:1.14.6",
		"index.docker.io/rancher/mirrored-coredns-coredns:1.14.6",
		"docker.io/rancher/mirrored-coredns-coredns:1.14.6",
	} {
		tag, err := name.NewTag(spelling)
		if err != nil {
			t.Fatalf("parsing %s: %v", spelling, err)
		}
		if tag.RegistryStr() != name.DefaultRegistry {
			t.Fatalf("%s no longer resolves to docker hub, so this test is not testing docker hub", spelling)
		}
		seeded, err := tarReference(tag)
		if err != nil {
			t.Fatalf("naming the seeded image for %s: %v", spelling, err)
		}
		if seeded.String() != "rancher/mirrored-coredns-coredns:1.14.6" {
			t.Fatalf("%s is seeded as %q. containerd normalizes the seeded name with docker's own "+
				"parser, and a pod asking for this image normalizes to "+
				"docker.io/rancher/mirrored-coredns-coredns:1.14.6, so a seeded name that normalizes "+
				"to anything else is an image the guest imports and no pod ever asks for",
				spelling, seeded.String())
		}
	}
}

func TestASingleComponentReferenceIsSeededUnderTheNameItWasGiven(t *testing.T) {
	tag, err := name.NewTag("busybox:1.37.0")
	if err != nil {
		t.Fatalf("parsing the reference: %v", err)
	}
	seeded, err := tarReference(tag)
	if err != nil {
		t.Fatalf("naming the seeded image: %v", err)
	}
	if seeded.String() != "busybox:1.37.0" {
		t.Fatalf("busybox is seeded as %q", seeded.String())
	}
}

func TestAnotherRegistryKeepsItsRegistryInTheSeededName(t *testing.T) {
	for _, spelling := range []string{"ghcr.io/example/thing:v1", "localhost:5000/example/thing:v1"} {
		tag, err := name.NewTag(spelling)
		if err != nil {
			t.Fatalf("parsing %s: %v", spelling, err)
		}
		seeded, err := tarReference(tag)
		if err != nil {
			t.Fatalf("naming the seeded image: %v", err)
		}
		if seeded.String() != spelling {
			t.Fatalf("an image from another registry is seeded as %q and not as %q, and dropping "+
				"that registry would make the seeded name a docker hub name the pod never asks for",
				seeded.String(), spelling)
		}
	}
}

func TestASeededTarballIsNamedAfterItsReference(t *testing.T) {
	if got := imageTarName("rancher/mirrored-coredns-coredns:1.14.6"); got != "rancher-mirrored-coredns-coredns-1.14.6.tar" {
		t.Fatalf("the guest would be handed %q", got)
	}
}

func TestTheSeededImagesLiveUnderTheConfiguredK3sDataDir(t *testing.T) {
	cfg := validConfig(t)
	cfg.StageDir = "/stage"
	cfg.K3sDataDir = "/var/lib/rancher/k3s"
	if got := guestImagesDir(cfg); got != "/stage/var/lib/rancher/k3s/agent/images" {
		t.Fatalf("the seeded images land at %q and k3s imports from the agent's images directory", got)
	}
}

func TestACachedTarballIsCheckedAgainstTheConfigBlobItShouldStartWith(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "image.tar")
	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatalf("building a synthetic image: %v", err)
	}
	configName, err := img.ConfigName()
	if err != nil {
		t.Fatalf("reading the config name: %v", err)
	}
	tag, err := name.NewTag("example/thing:v1", name.WithDefaultRegistry(""))
	if err != nil {
		t.Fatalf("parsing the reference: %v", err)
	}
	if err := tarball.WriteToFile(path, tag, img); err != nil {
		t.Fatalf("writing the tarball: %v", err)
	}
	if err := verifyImageTar(path, configName); err != nil {
		t.Fatalf("a tarball this builder wrote was refused: %v", err)
	}
	other, err := img.ConfigName()
	if err != nil {
		t.Fatalf("reading the config name: %v", err)
	}
	wrong := v1.Hash{Algorithm: other.Algorithm, Hex: strings.Repeat("0", len(other.Hex))}
	if err := verifyImageTar(path, wrong); err == nil {
		t.Fatal("a tarball whose first entry is not the image's config blob was accepted")
	}
}

func TestACachedTarballThatIsNotATarballIsRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "image.tar")
	if err := os.WriteFile(path, []byte("not a tar"), 0o644); err != nil {
		t.Fatalf("writing the file: %v", err)
	}
	configName, err := random.Image(1024, 1)
	if err != nil {
		t.Fatalf("building a synthetic image: %v", err)
	}
	blob, err := configName.ConfigName()
	if err != nil {
		t.Fatalf("reading the config name: %v", err)
	}
	if err := verifyImageTar(path, blob); err == nil {
		t.Fatal("a file that is not a tarball was accepted as a cached image")
	}
}
