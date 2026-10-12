package guestrootfs

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const epoch = 1790380800

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

const dpkgStatus = `Package: cloud-init
Status: install ok installed

Package: jq
Status: install ok installed

Package: ca-certificates
Status: install ok installed

Package: curl
Status: install ok installed

Package: iptables
Status: install ok installed

Package: openssh-server
Status: install ok installed

Package: tar
Status: install ok installed

Package: unzip
Status: deinstall ok config-files
`

type baseEntry struct {
	name  string
	typ   byte
	mode  int64
	body  string
	link  string
	mtime int64
	pax   map[string]string
}

func baseTar(t *testing.T, status string) []byte {
	t.Helper()
	entries := []baseEntry{
		{name: "./", typ: tar.TypeDir, mode: 0o755},
		{name: "./etc/", typ: tar.TypeDir, mode: 0o755},
		{name: "./etc/fstab", typ: tar.TypeReg, mode: 0o644, body: "LABEL=cloudimg-rootfs / ext4 defaults 0 1\nLABEL=UEFI /boot/efi vfat umask=0077 0 1\n"},
		{name: "./etc/machine-id", typ: tar.TypeReg, mode: 0o444, body: "0123456789abcdef0123456789abcdef\n"},
		{name: "./etc/ssh/", typ: tar.TypeDir, mode: 0o755},
		{name: "./etc/ssh/sshd_config", typ: tar.TypeReg, mode: 0o644, body: "Include /etc/ssh/sshd_config.d/*.conf\n"},
		{name: "./etc/ssh/ssh_host_ed25519_key", typ: tar.TypeReg, mode: 0o600, body: "PRIVATE KEY BAKED BY THE BASE\n"},
		{name: "./etc/ssh/ssh_host_ed25519_key.pub", typ: tar.TypeReg, mode: 0o644, body: "ssh-ed25519 AAAA base\n"},
		{name: "./etc/cloud/", typ: tar.TypeDir, mode: 0o755},
		{name: "./etc/cloud/cloud.cfg.d/", typ: tar.TypeDir, mode: 0o755},
		{name: "./usr/", typ: tar.TypeDir, mode: 0o755},
		{name: "./usr/bin/", typ: tar.TypeDir, mode: 0o755},
		{name: "./usr/bin/ping", typ: tar.TypeReg, mode: 0o755, body: "ELF ping", pax: map[string]string{"SCHILY.xattr.security.capability": "\x01\x00\x00\x02\x00\x20\x00\x00"}},
		{name: "./usr/bin/ping4", typ: tar.TypeLink, mode: 0o755, link: "./usr/bin/ping"},
		{name: "./usr/bin/future", typ: tar.TypeReg, mode: 0o755, body: "built after the epoch", mtime: epoch + 86400},
		{name: "./usr/local/", typ: tar.TypeDir, mode: 0o755},
		{name: "./usr/local/bin/", typ: tar.TypeDir, mode: 0o755},
		{name: "./var/", typ: tar.TypeDir, mode: 0o755},
		{name: "./var/lib/", typ: tar.TypeDir, mode: 0o755},
		{name: "./var/lib/cloud/", typ: tar.TypeDir, mode: 0o755},
		{name: "./var/lib/cloud/instance", typ: tar.TypeSymlink, mode: 0o777, link: "/var/lib/cloud/instances/base"},
		{name: "./var/lib/dpkg/", typ: tar.TypeDir, mode: 0o755},
		{name: "./var/lib/dpkg/status", typ: tar.TypeReg, mode: 0o644, body: status},
		{name: "./bin", typ: tar.TypeSymlink, mode: 0o777, link: "usr/bin"},
	}
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, e := range entries {
		mtime := e.mtime
		if mtime == 0 {
			mtime = epoch - 3600
		}
		h := &tar.Header{
			Name: e.name, Typeflag: e.typ, Mode: e.mode, Linkname: e.link, Size: int64(len(e.body)),
			ModTime: time.Unix(mtime, 0), Uname: "root", Gname: "root", PAXRecords: e.pax,
			AccessTime: time.Unix(mtime+5, 0), ChangeTime: time.Unix(mtime+7, 0), Format: tar.FormatPAX,
		}
		if e.typ != tar.TypeReg {
			h.Size = 0
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func dumbpipeAsset(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dumbpipe")
	if err := os.WriteFile(path, []byte("ELF dumbpipe"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func repoSpec(t *testing.T) Spec {
	t.Helper()
	b, err := LoadBuild(filepath.Join(repoRoot(t), "deploy", "guest-image", "rootfs.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := b.Spec(Options{SourceDateEpoch: epoch, Assets: map[string]string{"dumbpipe": dumbpipeAsset(t)}})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

type outEntry struct {
	hdr  *tar.Header
	body []byte
}

func compose(t *testing.T, base []byte, spec Spec) ([]byte, map[string]outEntry, Report) {
	t.Helper()
	var out bytes.Buffer
	report, err := Compose(bytes.NewReader(base), spec, &out)
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]outEntry{}
	r := tar.NewReader(bytes.NewReader(out.Bytes()))
	for {
		h, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		key := "/" + strings.TrimSuffix(strings.TrimPrefix(h.Name, "./"), "/")
		if _, dup := entries[key]; dup {
			t.Fatalf("%s appears twice in the composed rootfs", key)
		}
		entries[key] = outEntry{hdr: h, body: body}
	}
	return out.Bytes(), entries, report
}

func TestTheRootfsCarriesNoSshHostKeyAndEnablesGrowpart(t *testing.T) {
	_, entries, report := compose(t, baseTar(t, dpkgStatus), repoSpec(t))
	for path := range entries {
		if strings.HasPrefix(path, "/etc/ssh/ssh_host_") {
			t.Fatalf("the rootfs carries %s: every guest would share it", path)
		}
		if strings.HasPrefix(path, "/var/lib/cloud/") {
			t.Fatalf("the rootfs carries cloud-init state %s from the base's own first boot", path)
		}
	}
	for _, e := range report.Entries {
		if strings.HasPrefix(e.Path, "/etc/ssh/ssh_host_") {
			t.Fatalf("the file list names %s", e.Path)
		}
	}
	if _, ok := entries["/etc/ssh/sshd_config"]; !ok {
		t.Fatal("removing the host keys removed the rest of /etc/ssh")
	}
	cfg, ok := entries["/etc/cloud/cloud.cfg.d/90-socialweb-guest.cfg"]
	if !ok {
		t.Fatal("the rootfs carries no cloud.cfg.d drop-in of ours")
	}
	for _, want := range []string{"growpart:", "mode: auto", "devices: [\"/\"]", "resize_rootfs: true", "ssh_deletekeys: true"} {
		if !strings.Contains(string(cfg.body), want) {
			t.Fatalf("the cloud.cfg drop-in does not say %q:\n%s", want, cfg.body)
		}
	}
	fstab := string(entries["/etc/fstab"].body)
	if strings.Contains(fstab, "/boot/efi") || !strings.HasPrefix(fstab, "LABEL="+RootfsLabel+"\t/\t") {
		t.Fatalf("fstab still mounts what the base did:\n%s", fstab)
	}
	if id := entries["/etc/machine-id"]; len(id.body) != 0 {
		t.Fatalf("the rootfs carries the base's machine-id %q", id.body)
	}
	if report.SSHHostKeys != 0 {
		t.Fatalf("report counts %d ssh host keys", report.SSHHostKeys)
	}
}

func TestTheImageCarriesThePreinstallManifest(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "bidder", "preinstall-market.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Packages []string `json:"packages"`
		Files    []struct {
			Path    string `json:"path"`
			Mode    string `json:"mode"`
			Content string `json:"content"`
		} `json:"files"`
		Runs []string `json:"runs"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	_, entries, report := compose(t, baseTar(t, dpkgStatus), repoSpec(t))
	for _, f := range manifest.Files {
		got, ok := entries[f.Path]
		if !ok {
			t.Fatalf("the rootfs does not carry the preinstall file %s", f.Path)
		}
		if string(got.body) != f.Content || got.hdr.Uid != 0 || got.hdr.Gid != 0 {
			t.Fatalf("%s differs from the preinstall manifest", f.Path)
		}
	}
	for _, p := range manifest.Packages {
		found := false
		for _, have := range report.Bake.Packages {
			found = found || have == p
		}
		if !found {
			t.Fatalf("the bake record does not name the preinstall package %s: %v", p, report.Bake.Packages)
		}
	}
	if len(report.Bake.Runs) != len(manifest.Runs) {
		t.Fatalf("the bake record accounts for %d of the manifest's %d runs", len(report.Bake.Runs), len(manifest.Runs))
	}
	dp, ok := entries["/usr/local/bin/dumbpipe"]
	if !ok || string(dp.body) != "ELF dumbpipe" || dp.hdr.Mode&0o777 != 0o755 {
		t.Fatal("the dumbpipe asset is not baked at /usr/local/bin/dumbpipe as an executable")
	}
	if v := string(entries["/usr/local/bin/dumbpipe.version"].body); v != "0.39.0\n" {
		t.Fatalf("dumbpipe.version is %q", v)
	}
}

func TestAPackageTheBaseDoesNotCarryIsRefused(t *testing.T) {
	status := strings.Replace(dpkgStatus, "Package: jq\nStatus: install ok installed\n\n", "", 1)
	var out bytes.Buffer
	_, err := Compose(bytes.NewReader(baseTar(t, status)), repoSpec(t), &out)
	if err == nil || !strings.Contains(err.Error(), "jq") {
		t.Fatalf("a base without jq was composed: %v", err)
	}
}

func TestARunTheBuildDoesNotAccountForIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "preinstall.json"), []byte(`{"packages":["jq"],"files":[],"runs":["apt-get install -y something"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rootfs.json"), []byte(`{"preinstall":"preinstall.json","runs":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := LoadBuild(filepath.Join(dir, "rootfs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Spec(Options{SourceDateEpoch: epoch}); err == nil {
		t.Fatal("a preinstall run with no replacement was accepted, so the image would silently skip it")
	}
}

func TestTwoComposesAreByteIdenticalAndClampedToTheEpoch(t *testing.T) {
	base := baseTar(t, dpkgStatus)
	a, entries, report := compose(t, base, repoSpec(t))
	b, _, _ := compose(t, base, repoSpec(t))
	if !bytes.Equal(a, b) {
		t.Fatal("two composes of the same inputs differ")
	}
	for path, e := range entries {
		if e.hdr.ModTime.Unix() > epoch {
			t.Fatalf("%s carries mtime %d after SOURCE_DATE_EPOCH %d", path, e.hdr.ModTime.Unix(), epoch)
		}
		if !e.hdr.AccessTime.IsZero() || !e.hdr.ChangeTime.IsZero() {
			t.Fatalf("%s carries an atime or ctime", path)
		}
	}
	ping := entries["/usr/bin/ping"]
	if ping.hdr.PAXRecords["SCHILY.xattr.security.capability"] == "" {
		t.Fatal("the base's file capability on ping was dropped")
	}
	if l := entries["/usr/bin/ping4"]; l.hdr.Typeflag != tar.TypeLink || l.hdr.Linkname != "./usr/bin/ping" {
		t.Fatalf("the hard link was not kept: %+v", l.hdr)
	}
	sum := sha256.Sum256(a)
	if report.TarSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("report names tar %s, the bytes hash to %x", report.TarSHA256, sum)
	}
	if report.DataBytes <= 0 {
		t.Fatal("report carries no data size to size the filesystem from")
	}
}
