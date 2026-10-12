package tsbundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const cacheTrailer = "\n// denoCacheMetadata="

var (
	seedRoots      = []string{"remote", "npm"}
	keptHeaders    = []string{"content-type", "location", "x-typescript-types", "x-deno-warning"}
	jsrPackageMeta = regexp.MustCompile(`^https://jsr\.io/(@[^/]+/[^/]+)/meta\.json$`)
	volatileNames  = []string{".scripts-warned-", ".deno_sync_lock"}
)

func volatile(name string) bool {
	for _, v := range volatileNames {
		if strings.HasPrefix(name, v) {
			return true
		}
	}
	return false
}

func seedFrom(fill, seed string, pins lockPins, epoch int64) error {
	if err := os.MkdirAll(seed, 0o755); err != nil {
		return err
	}
	for _, sub := range seedRoots {
		from := filepath.Join(fill, sub)
		if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(fill, p)
			if err != nil {
				return err
			}
			dst := filepath.Join(seed, rel)
			info, err := os.Lstat(p)
			if err != nil {
				return err
			}
			switch {
			case info.IsDir():
				return os.MkdirAll(dst, 0o755)
			case volatile(d.Name()):
				return nil
			case info.Mode()&fs.ModeSymlink != 0:
				t, err := os.Readlink(p)
				if err != nil {
					return err
				}
				return os.Symlink(t, dst)
			}
			mode := os.FileMode(0o644)
			if info.Mode().Perm()&0o111 != 0 {
				mode = 0o755
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			switch {
			case sub == "remote":
				b = normalizeRemote(b, pins, epoch)
			case d.Name() == "registry.json":
				b = normalizePackument(b, pins)
			}
			return os.WriteFile(dst, b, mode)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func decodeJSON(b []byte) (map[string]any, bool) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, false
	}
	return m, true
}

func normalizeRemote(b []byte, pins lockPins, epoch int64) []byte {
	i := bytes.LastIndex(b, []byte(cacheTrailer))
	if i < 0 {
		return b
	}
	body, raw := b[:i], bytes.TrimRight(b[i+len(cacheTrailer):], "\n")
	meta, ok := decodeJSON(raw)
	if !ok {
		return b
	}
	headers, _ := meta["headers"].(map[string]any)
	kept := map[string]any{}
	for _, h := range keptHeaders {
		if v, ok := headers[h]; ok {
			kept[h] = v
		}
	}
	meta["headers"] = kept
	meta["time"] = epoch
	if u, _ := meta["url"].(string); u != "" {
		if m := jsrPackageMeta.FindStringSubmatch(u); m != nil {
			body = prunePackageMeta(body, pins.jsr[m[1]])
		}
	}
	out, err := json.Marshal(meta)
	if err != nil {
		return b
	}
	return append(append(append([]byte{}, body...), cacheTrailer...), out...)
}

func prunePackageMeta(body []byte, keep map[string]bool) []byte {
	doc, ok := decodeJSON(body)
	if !ok || len(keep) == 0 {
		return body
	}
	versions, _ := doc["versions"].(map[string]any)
	pruned := map[string]any{}
	for v, x := range versions {
		if keep[v] {
			pruned[v] = x
		}
	}
	doc["versions"] = pruned
	if latest, _ := doc["latest"].(string); !keep[latest] {
		names := make([]string, 0, len(pruned))
		for v := range pruned {
			names = append(names, v)
		}
		sort.Strings(names)
		if len(names) > 0 {
			doc["latest"] = names[len(names)-1]
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

func normalizePackument(b []byte, pins lockPins) []byte {
	doc, ok := decodeJSON(b)
	if !ok {
		return b
	}
	delete(doc, "time")
	delete(doc, "_deno.etag")
	name, _ := doc["name"].(string)
	if keep := pins.npm[name]; len(keep) > 0 {
		versions, _ := doc["versions"].(map[string]any)
		pruned := map[string]any{}
		for v, x := range versions {
			if keep[v] {
				pruned[v] = x
			}
		}
		doc["versions"] = pruned
		tags, _ := doc["dist-tags"].(map[string]any)
		kept := map[string]any{}
		for t, v := range tags {
			if s, _ := v.(string); keep[s] {
				kept[t] = v
			}
		}
		doc["dist-tags"] = kept
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return b
	}
	return out
}
