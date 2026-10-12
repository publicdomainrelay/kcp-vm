package tsbundle

import (
	"archive/tar"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/assets"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/denorun"
)

const (
	ArchiveName   = "closure.tar.zst"
	ManifestName  = "manifest.json.zst"
	FormatVersion = 1
	SourceDir     = "src"
	SeedDir       = "deno"
)

var sha256Name = regexp.MustCompile(`^[0-9a-f]{64}$`)

type RepoRecord struct {
	Name     string   `json:"name"`
	Commit   string   `json:"commit"`
	Dirty    bool     `json:"dirty"`
	Prefixes []string `json:"prefixes"`
}

type EntryRecord struct {
	EntrySpec
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

type LockRecord struct {
	Path  string `json:"path"`
	State string `json:"state"`
}

type FileRecord struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
}

type Manifest struct {
	Format          int           `json:"format"`
	Platform        string        `json:"platform"`
	Deno            string        `json:"deno"`
	Release         bool          `json:"release"`
	Offline         bool          `json:"offline"`
	SourceDateEpoch int64         `json:"sourceDateEpoch"`
	TreeSHA256      string        `json:"treeSha256"`
	ArchiveSHA256   string        `json:"archiveSha256"`
	ArchiveBytes    int64         `json:"archiveBytes"`
	Repos           []RepoRecord  `json:"repos"`
	Entries         []EntryRecord `json:"entries"`
	Lockfiles       []LockRecord  `json:"lockfiles"`
	Files           []FileRecord  `json:"files"`
}

func Pack(tree string, epoch int64) ([]byte, Manifest, error) {
	m := Manifest{Format: FormatVersion, SourceDateEpoch: epoch, Repos: []RepoRecord{}, Entries: []EntryRecord{}, Lockfiles: []LockRecord{}, Files: []FileRecord{}}
	var raw bytes.Buffer
	zw, err := zstd.NewWriter(&raw, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, m, err
	}
	tw := tar.NewWriter(zw)
	mtime := time.Unix(epoch, 0).UTC()
	err = filepath.WalkDir(tree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(tree, p)
		if err != nil || rel == "." {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		hdr := &tar.Header{Name: rel, ModTime: mtime}
		rec := FileRecord{Path: rel}
		switch {
		case info.IsDir():
			hdr.Typeflag, hdr.Name, hdr.Mode = tar.TypeDir, rel+"/", 0o755
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			target = filepath.ToSlash(target)
			if err := safeLink(rel, target); err != nil {
				return err
			}
			hdr.Typeflag, hdr.Linkname, hdr.Mode = tar.TypeSymlink, target, 0o777
			rec.Kind, rec.Target = "l", target
		case info.Mode().IsRegular():
			hdr.Typeflag, hdr.Size, hdr.Mode = tar.TypeReg, info.Size(), 0o444
			rec.Kind = "f"
			if info.Mode().Perm()&0o111 != 0 {
				hdr.Mode, rec.Kind = 0o555, "x"
			}
			sum, err := fileSHA256(p)
			if err != nil {
				return err
			}
			rec.SHA256 = sum
		default:
			return fmt.Errorf("tsbundle: %s is neither a file, a directory nor a symlink", p)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if hdr.Typeflag == tar.TypeDir {
			return nil
		}
		m.Files = append(m.Files, rec)
		if hdr.Typeflag != tar.TypeReg {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
	if err != nil {
		zw.Close()
		return nil, m, err
	}
	if err := errors.Join(tw.Close(), zw.Close()); err != nil {
		return nil, m, err
	}
	if m.TreeSHA256, err = assets.TreeDigest(tree); err != nil {
		return nil, m, err
	}
	sum := sha256.Sum256(raw.Bytes())
	m.ArchiveSHA256, m.ArchiveBytes = hex.EncodeToString(sum[:]), int64(raw.Len())
	return raw.Bytes(), m, nil
}

func safeLink(rel, target string) error {
	if path.IsAbs(target) {
		return fmt.Errorf("tsbundle: symlink %s -> %s is absolute", rel, target)
	}
	joined := path.Clean(path.Join(path.Dir(rel), target))
	if joined == ".." || strings.HasPrefix(joined, "../") {
		return fmt.Errorf("tsbundle: symlink %s -> %s escapes the closure", rel, target)
	}
	return nil
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (m Manifest) Marshal() ([]byte, error) {
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	zw, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return nil, err
	}
	defer zw.Close()
	return zw.EncodeAll(append(b, '\n'), nil), nil
}

func UnmarshalManifest(b []byte) (Manifest, error) {
	var m Manifest
	zr, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return m, err
	}
	defer zr.Close()
	raw, err := zr.DecodeAll(b, nil)
	if err != nil {
		return m, err
	}
	err = json.Unmarshal(raw, &m)
	return m, err
}

func WriteBundle(out string, archive []byte, m Manifest) error {
	b, err := m.Marshal()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, ArchiveName), archive, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, ManifestName), b, 0o644)
}

type Embedded struct {
	FS  fs.FS
	Dir string
}

func (Embedded) Kind() string { return KindEmbedded }

func (Embedded) Production() bool { return true }

func (e Embedded) Manifest() (Manifest, error) {
	var m Manifest
	if e.FS == nil || e.Dir == "" {
		return m, errors.New("tsbundle: this build embeds no TypeScript closure for its platform")
	}
	b, err := fs.ReadFile(e.FS, path.Join(e.Dir, ManifestName))
	if err != nil {
		return m, fmt.Errorf("tsbundle: this build embeds no TypeScript closure (run cmd/socialweb-tsbundle, then rebuild with -tags release): %w", err)
	}
	if m, err = UnmarshalManifest(b); err != nil {
		return m, fmt.Errorf("tsbundle: embedded %s: %w", ManifestName, err)
	}
	if m.Format != FormatVersion || !sha256Name.MatchString(m.TreeSHA256) || !sha256Name.MatchString(m.ArchiveSHA256) {
		return m, fmt.Errorf("tsbundle: embedded %s has format %d, tree %q, archive %q", ManifestName, m.Format, m.TreeSHA256, m.ArchiveSHA256)
	}
	return m, nil
}

func (e Embedded) Prepare(stateDir string) (Prepared, error) {
	m, err := e.Manifest()
	if err != nil {
		return Prepared{}, err
	}
	state, err := absState(stateDir)
	if err != nil {
		return Prepared{}, err
	}
	tsRoot := filepath.Join(state, "ts")
	dst := filepath.Join(tsRoot, m.TreeSHA256)
	if _, err := os.Lstat(dst); err == nil {
		if err := Verify(dst, m); err != nil {
			return Prepared{}, err
		}
	} else if err := e.extract(m, tsRoot, dst); err != nil {
		return Prepared{}, err
	}
	denoDirs := filepath.Join(state, "deno")
	if err := seed(filepath.Join(dst, SeedDir), denoDirs, m.TreeSHA256); err != nil {
		return Prepared{}, err
	}
	return Prepared{Root: filepath.Join(dst, SourceDir), Key: m.TreeSHA256, Mode: denorun.Production, DenoDirs: denoDirs}, nil
}

func (e Embedded) extract(m Manifest, tsRoot, dst string) error {
	archive, err := fs.ReadFile(e.FS, path.Join(e.Dir, ArchiveName))
	if err != nil {
		return fmt.Errorf("tsbundle: embedded %s: %w", ArchiveName, err)
	}
	if sum := sha256.Sum256(archive); hex.EncodeToString(sum[:]) != m.ArchiveSHA256 {
		return fmt.Errorf("tsbundle: embedded %s hashes to %x, its manifest records %s; refused", ArchiveName, sum, m.ArchiveSHA256)
	}
	if err := os.MkdirAll(tsRoot, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(tsRoot, ".tmp-"+randomSuffix())
	if err := os.Mkdir(tmp, 0o755); err != nil {
		return err
	}
	zr, err := zstd.NewReader(bytes.NewReader(archive), zstd.WithDecoderConcurrency(1))
	if err != nil {
		return err
	}
	defer zr.Close()
	if err := assets.ExtractTar(zr, tmp); err != nil {
		assets.RemoveTree(tmp)
		return fmt.Errorf("tsbundle: unpacking the closure: %w", err)
	}
	if got, err := assets.TreeDigest(tmp); err != nil || got != m.TreeSHA256 {
		assets.RemoveTree(tmp)
		return fmt.Errorf("tsbundle: the closure unpacks to tree %s, its manifest records %s (%v)", got, m.TreeSHA256, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		assets.RemoveTree(tmp)
		if verr := Verify(dst, m); verr == nil {
			return nil
		}
		return fmt.Errorf("tsbundle: placing %s: %w", dst, err)
	}
	return readOnlyDirs(dst)
}

func readOnlyDirs(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			dirs = append(dirs, p)
		}
		return err
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], 0o555); err != nil {
			return err
		}
	}
	return nil
}

func Verify(dir string, m Manifest) error {
	got, err := assets.TreeDigest(dir)
	if err == nil && got == m.TreeSHA256 {
		return nil
	}
	for _, f := range m.Files {
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		info, lerr := os.Lstat(p)
		switch {
		case lerr != nil:
			return fmt.Errorf("tsbundle: extracted closure file %s is gone (%v); refused (remove %s to extract again)", f.Path, lerr, dir)
		case f.Kind == "l":
			if t, _ := os.Readlink(p); filepath.ToSlash(t) != f.Target {
				return fmt.Errorf("tsbundle: extracted closure symlink %s changed; refused (remove %s to extract again)", f.Path, dir)
			}
		default:
			sum, serr := fileSHA256(p)
			exec := info.Mode().Perm()&0o111 != 0
			if serr != nil || sum != f.SHA256 || exec != (f.Kind == "x") {
				return fmt.Errorf("tsbundle: extracted closure file %s changed (sha256 %s, manifest %s); refused (remove %s to extract again)", f.Path, sum, f.SHA256, dir)
			}
		}
	}
	return fmt.Errorf("tsbundle: extracted closure %s hashes to tree %s, manifest %s (an added file or directory); refused (%v)", dir, got, m.TreeSHA256, err)
}

func seed(from, denoDirs, key string) error {
	dst := filepath.Join(denoDirs, key)
	if _, err := os.Lstat(dst); err == nil {
		return nil
	}
	if err := os.MkdirAll(denoDirs, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(denoDirs, ".tmp-"+randomSuffix())
	if err := copyTree(from, tmp); err != nil {
		assets.RemoveTree(tmp)
		return fmt.Errorf("tsbundle: seeding DENO_DIR %s: %w", dst, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		assets.RemoveTree(tmp)
		if _, serr := os.Lstat(dst); serr == nil {
			return nil
		}
		return err
	}
	return nil
}

func copyTree(from, to string) error {
	if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
		return os.MkdirAll(to, 0o755)
	}
	return filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, p)
		if err != nil {
			return err
		}
		dst := filepath.Join(to, rel)
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case info.IsDir():
			return os.MkdirAll(dst, 0o755)
		case info.Mode()&fs.ModeSymlink != 0:
			t, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(t, dst)
		default:
			mode := os.FileMode(0o644)
			if info.Mode().Perm()&0o111 != 0 {
				mode = 0o755
			}
			return copyFile(p, dst, mode)
		}
	})
}

func copyFile(from, to string, mode os.FileMode) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	_, cerr := io.Copy(out, in)
	return errors.Join(cerr, out.Close())
}

func randomSuffix() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
