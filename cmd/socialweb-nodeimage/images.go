package main

import (
	"archive/tar"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/nodefirecracker"
)

const (
	imagePlatformOS = "linux"

	imagePlatformArch = "amd64"

	imageTarSuffix = ".tar"

	imageUserAgent = "socialweb-nodeimage"

	imagePullTimeout = 30 * time.Minute
)

type imageTar struct {
	reference string

	repository string

	digest string

	platform string

	guestName string

	cachePath string

	bytes int64
}

type imageReference struct {
	tag name.Tag

	digest name.Digest

	hasTag bool

	seededAs string

	text string
}

func (r imageReference) pinned() bool {
	return r.hasTag && r.digest.DigestStr() != ""
}

func parseImageReference(reference string) (imageReference, error) {
	at := strings.LastIndex(reference, "@")
	if at < 0 {
		tag, err := name.NewTag(reference)
		if err != nil {
			return imageReference{}, unparseableReference(reference, err)
		}
		return imageReference{tag: tag, hasTag: true, seededAs: tag.String(), text: reference}, nil
	}
	digest, err := name.NewDigest(reference)
	if err != nil {
		return imageReference{}, unparseableReference(reference, err)
	}
	one := imageReference{digest: digest, text: reference}
	base := reference[:at]
	if strings.LastIndex(base, ":") < strings.LastIndex(base, "/") {
		return one, nil
	}
	tag, err := name.NewTag(base)
	if err != nil {
		return imageReference{}, unparseableReference(reference, err)
	}
	one.tag = tag
	one.hasTag = true
	one.seededAs = tag.String()
	return one, nil
}

func unparseableReference(reference string, cause error) error {
	return fmt.Errorf("socialweb-nodeimage: the images entry %q does not parse as a container "+
		"image reference: %w -- every entry is turned into one tarball that the guest's containerd "+
		"imports under that exact name, so an entry that cannot be parsed is either a tarball the "+
		"guest's kubelet never asks for or a reference whose name in the guest is not the name the "+
		"k3s manifests use", reference, cause)
}

func pinnedSpelling(seededAs string, digest v1.Hash) string {
	return seededAs + "@" + digest.String()
}

const unpinnedTagWhy = "A tag is not an identity: the registry it lives in can be pushed to at any " +
	"time, and when it is, this entry is unchanged, the name this image is given is unchanged, and " +
	"the image the store already holds under that name is not the one the tag now resolves to -- so " +
	"a node booted from it runs the images of a previous release under the name of the current one, " +
	"and nothing in the cluster says so, because a cluster whose images are all one release old is " +
	"as internally consistent as one that is current"

func refuseUnpinned(one imageReference) error {
	if !one.hasTag {
		return taglessReferenceRefusal(one)
	}
	digest, err := resolveTagDigest(one.tag)
	if err != nil {
		return unresolvedTagRefusal(one, err)
	}
	return unpinnedTagRefusal(one, digest)
}

func unpinnedTagRefusal(one imageReference, digest v1.Hash) error {
	return fmt.Errorf("socialweb-nodeimage: the images entry %q names %s by tag and not by digest, "+
		"so the digest is not one of the inputs this image's name is made of and a registry can "+
		"change what this entry means without changing the entry. %s. This refusal resolved the "+
		"tag, and it resolves to %s now, so write the entry as %s: the digest is then an input, a "+
		"re-pushed tag changes the name rather than the image behind it, and the store builds a "+
		"second image instead of serving the first", one.text, one.text, unpinnedTagWhy,
		digest.String(), pinnedSpelling(one.seededAs, digest))
}

func unresolvedTagRefusal(one imageReference, cause error) error {
	return fmt.Errorf("socialweb-nodeimage: the images entry %q names %s by tag and not by digest, "+
		"so the digest is not one of the inputs this image's name is made of and a registry can "+
		"change what this entry means without changing the entry. %s. Write it as %s@<the digest it "+
		"resolves to>, and this builder tried to resolve that tag so this refusal could name the "+
		"digest and could not: %w -- resolving needs the registry, so on a host that cannot reach "+
		"one the digest has to come from a host that can, e.g. from the manifest of an image built "+
		"from this tag before now", one.text, one.text, unpinnedTagWhy, one.seededAs, cause)
}

func taglessReferenceRefusal(one imageReference) error {
	return fmt.Errorf("socialweb-nodeimage: the images entry %q names an image by digest and "+
		"carries no tag, and every entry has to name its image as the tag the guest asks for AND "+
		"the digest that tag resolved to. containerd matches what a pod asks for against the tags "+
		"in its image store, and every k3s manifest asks by tag: an image seeded under a digest "+
		"only is one nothing ever asks for, so the pod falls through to a pull, and in a guest "+
		"with no resolver that pull does not fail, it waits -- which reads as a pull that is "+
		"merely slow rather than as an image that is present and unreachable. The digest cannot "+
		"say which tag the manifests use, so write the tag the k3s manifest references into the "+
		"entry: %s:<that tag>@%s", one.text, one.digest.Context().Name(), one.digest.DigestStr())
}

func guestImagesDir(cfg Config) string {
	return filepath.Join(cfg.StageDir, cfg.K3sDataDir, "agent", "images")
}

func imageTarName(reference string) string {
	return strings.NewReplacer("/", "-", ":", "-").Replace(reference) + imageTarSuffix
}

func imagePlatform() v1.Platform {
	return v1.Platform{OS: imagePlatformOS, Architecture: imagePlatformArch}
}

func imagePlatformString() string {
	return imagePlatformOS + "/" + imagePlatformArch
}

func resolveTagDigest(tag name.Tag) (v1.Hash, error) {
	ctx, cancel := context.WithTimeout(context.Background(), imagePullTimeout)
	defer cancel()
	desc, err := remote.Get(tag,
		remote.WithPlatform(imagePlatform()),
		remote.WithContext(ctx),
		remote.WithAuth(authn.Anonymous),
		remote.WithUserAgent(imageUserAgent))
	if err != nil {
		return v1.Hash{}, err
	}
	img, err := desc.Image()
	if err != nil {
		return v1.Hash{}, err
	}
	return img.Digest()
}

func checkImagePins(cfg Config) error {
	for _, reference := range cfg.Images {
		one, err := parseImageReference(reference)
		if err != nil {
			return err
		}
		if one.pinned() {
			continue
		}
		return refuseUnpinned(one)
	}
	return nil
}

func pullImageTars(cfg Config) ([]imageTar, error) {
	if err := os.MkdirAll(cfg.ImageCacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("socialweb-nodeimage: creating the image cache at %s: %w -- the cache "+
			"is keyed by the digest each reference resolves to, so a rebuild that resolves to the "+
			"digest it resolved to last time downloads nothing; a builder that cannot write it "+
			"re-pulls every layer on every run, and an image builder is run far more often than the "+
			"images it seeds change", cfg.ImageCacheDir, err)
	}
	pulled := make([]imageTar, 0, len(cfg.Images))
	for _, reference := range cfg.Images {
		one, err := pullImageTar(cfg.ImageCacheDir, reference)
		if err != nil {
			return nil, err
		}
		pulled = append(pulled, one)
	}
	return pulled, nil
}

func pullImageTar(cacheDir, reference string) (imageTar, error) {
	one, err := parseImageReference(reference)
	if err != nil {
		return imageTar{}, err
	}
	if !one.pinned() {
		return imageTar{}, refuseUnpinned(one)
	}
	tag := one.tag
	ctx, cancel := context.WithTimeout(context.Background(), imagePullTimeout)
	defer cancel()
	desc, err := remote.Get(one.digest,
		remote.WithPlatform(imagePlatform()),
		remote.WithContext(ctx),
		remote.WithAuth(authn.Anonymous),
		remote.WithUserAgent(imageUserAgent))
	if err != nil {
		return imageTar{}, fmt.Errorf("socialweb-nodeimage: fetching %s: %w -- this build has to "+
			"reach the registry because it is the only thing that can: the guest it produces is "+
			"built to run these images without ever reaching a registry itself. The entry names "+
			"the image by digest, so the registry either serves that exact manifest or this fails "+
			"here rather than in the guest, and a digest a registry no longer serves -- a tag "+
			"re-pushed since this entry was written, or a manifest the registry has collected -- "+
			"is answered by resolving the tag again and writing the digest it now has. No "+
			"credential is sent, so a private image and a rate-limited Docker Hub both fail here "+
			"rather than in the guest", reference, err)
	}
	img, err := desc.Image()
	if err != nil {
		return imageTar{}, fmt.Errorf("socialweb-nodeimage: %s carries no %s image: %w -- the guest "+
			"boots this host's kernel and runs this host's architecture, and an image for another "+
			"platform would be seeded into a guest whose kubelet cannot execute it, which reads as "+
			"a pod that never starts rather than as an image that was never built for it",
			reference, imagePlatformString(), err)
	}
	config, err := img.ConfigFile()
	if err != nil {
		return imageTar{}, fmt.Errorf("socialweb-nodeimage: reading the config of %s: %w", reference, err)
	}
	if config.OS != imagePlatformOS || config.Architecture != imagePlatformArch {
		return imageTar{}, fmt.Errorf("socialweb-nodeimage: %s names a %s/%s image and this builder "+
			"seeds %s. The digest names another platform's manifest, or an index whose entry for "+
			"this platform is a different image, and either way the guest would import an image "+
			"its kubelet cannot run; the tarball's name would be right and the pod would still "+
			"never start", reference, config.OS, config.Architecture, imagePlatformString())
	}
	digest, err := img.Digest()
	if err != nil {
		return imageTar{}, fmt.Errorf("socialweb-nodeimage: digesting %s: %w", reference, err)
	}
	configName, err := img.ConfigName()
	if err != nil {
		return imageTar{}, fmt.Errorf("socialweb-nodeimage: %w", err)
	}
	named, err := tarReference(tag)
	if err != nil {
		return imageTar{}, fmt.Errorf("socialweb-nodeimage: working out the name %s has inside its "+
			"tarball: %w", reference, err)
	}
	guestName := imageTarName(tag.String())
	cachePath := filepath.Join(cacheDir, strings.ReplaceAll(digest.String(), ":", "-")+imageTarSuffix)
	tar := imageTar{
		reference:  reference,
		repository: tag.Context().Name(),
		digest:     digest.String(),
		platform:   config.OS + "/" + config.Architecture,
		guestName:  guestName,
		cachePath:  cachePath,
	}
	st, statErr := os.Stat(cachePath)
	if statErr == nil && st.Size() > 0 {
		if err := verifyImageTar(cachePath, configName); err != nil {
			return imageTar{}, fmt.Errorf("socialweb-nodeimage: the cached tarball for %s at %s: %w "+
				"-- the cache is written under the digest it was pulled at and moved into place "+
				"whole, so a file there that is not the tarball this reference digests to was "+
				"either truncated before it was moved or is not this builder's file, and seeding "+
				"the guest from it would produce a guest whose containerd import fails at boot",
				reference, cachePath, err)
		}
		tar.bytes = st.Size()
		fmt.Printf("image %s at %s (cached, %d bytes)\n", reference, digest.String(), st.Size())
		return tar, nil
	}
	written, err := writeImageTar(cachePath, named, img)
	if err != nil {
		return imageTar{}, fmt.Errorf("socialweb-nodeimage: pulling %s into %s: %w -- the tarball is "+
			"what the guest's containerd imports locally, and its absence is only discovered as a "+
			"system pod stuck pulling from a registry the guest cannot resolve", reference, cachePath, err)
	}
	tar.bytes = written
	fmt.Printf("image %s at %s (%d bytes)\n", reference, digest.String(), written)
	return tar, nil
}

func tarReference(tag name.Tag) (name.Tag, error) {
	if tag.RegistryStr() != name.DefaultRegistry {
		return tag, nil
	}
	original := tag.String()
	at := strings.Index(original, "/")
	if at <= 0 || !strings.ContainsAny(original[:at], ".:") {
		return tag, nil
	}
	return name.NewTag(tag.RepositoryStr()+":"+tag.Identifier(), name.WithDefaultRegistry(""))
}

func writeImageTar(cachePath string, ref name.Tag, img v1.Image) (int64, error) {
	tmp, err := os.CreateTemp(filepath.Dir(cachePath), filepath.Base(cachePath)+".partial-")
	if err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return 0, err
	}
	if err := tarball.WriteToFile(tmp.Name(), ref, img); err != nil {
		os.Remove(tmp.Name())
		return 0, err
	}
	if err := os.Rename(tmp.Name(), cachePath); err != nil {
		os.Remove(tmp.Name())
		return 0, err
	}
	st, err := os.Stat(cachePath)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

func verifyImageTar(path string, configName v1.Hash) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	header, err := tar.NewReader(f).Next()
	if err != nil {
		return err
	}
	if header.Name != configName.String() {
		return fmt.Errorf("its first entry is %q and the image's config blob is %q",
			header.Name, configName.String())
	}
	return nil
}

func stageImageTars(cfg Config, tars []imageTar) error {
	dir := guestImagesDir(cfg)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("socialweb-nodeimage: creating %s: %w -- that is the directory k3s's own "+
			"agent imports image tarballs from before it starts its kubelet, and it is derived "+
			"from the k3s_data_dir in this config rather than written down twice", dir, err)
	}
	for _, t := range tars {
		dst := filepath.Join(dir, t.guestName)
		if err := copyFile(t.cachePath, dst, 0o644); err != nil {
			return fmt.Errorf("socialweb-nodeimage: seeding %s into %s: %w -- the guest's containerd "+
				"imports whatever tarballs are in that directory before its kubelet starts, so an "+
				"image missing there is not a missing image: it is a system pod that reaches for a "+
				"registry the guest has no resolver for and never arrives", t.reference, dst, err)
		}
	}
	return nil
}

func manifestImages(tars []imageTar) []nodefirecracker.SeededImage {
	seeded := make([]nodefirecracker.SeededImage, 0, len(tars))
	for _, t := range tars {
		seeded = append(seeded, nodefirecracker.SeededImage{
			Reference:  t.reference,
			Repository: t.repository,
			Digest:     t.digest,
			Platform:   t.platform,
			TarBytes:   t.bytes,
		})
	}
	return seeded
}
