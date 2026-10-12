package tsbuild

import (
	"archive/tar"
	"bytes"
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
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/assets"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/tsbundle"
)

func Pack(tree string, epoch int64) ([]byte, tsbundle.Manifest, error) {
	m := tsbundle.Manifest{Format: tsbundle.FormatVersion, SourceDateEpoch: epoch, Repos: []tsbundle.RepoRecord{}, Entries: []tsbundle.EntryRecord{}, Lockfiles: []tsbundle.LockRecord{}, Files: []tsbundle.FileRecord{}}
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
		rec := tsbundle.FileRecord{Path: rel}
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
			sum, err := tsbundle.FileSHA256(p)
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

func MarshalManifest(m tsbundle.Manifest) ([]byte, error) {
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

func WriteBundle(out string, archive []byte, m tsbundle.Manifest) error {
	b, err := MarshalManifest(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, tsbundle.ArchiveName), archive, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(out, tsbundle.ManifestName), b, 0o644)
}
