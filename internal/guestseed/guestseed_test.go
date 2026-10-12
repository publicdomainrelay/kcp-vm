package guestseed

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

func sampleSeed() Seed {
	big := strings.Repeat("#cloud-config filler line that makes this document span many clusters\n", 4000)
	return Seed{
		UserData:      []byte("#cloud-config\nruncmd:\n  - echo hello\n" + big),
		MetaData:      []byte("instance-id: fc-test\nlocal-hostname: fc-test\n"),
		NetworkConfig: []byte("version: 2\nethernets:\n  eth0:\n    dhcp4: false\n"),
	}
}

type fatFile struct {
	name string
	body []byte
}

func readFAT(t *testing.T, img []byte) (string, string, []fatFile) {
	t.Helper()
	if len(img) < 512 || img[510] != 0x55 || img[511] != 0xAA {
		t.Fatalf("no boot sector signature")
	}
	bps := int(binary.LittleEndian.Uint16(img[11:13]))
	spc := int(img[13])
	reserved := int(binary.LittleEndian.Uint16(img[14:16]))
	nfats := int(img[16])
	rootEntries := int(binary.LittleEndian.Uint16(img[17:19]))
	fatSectors := int(binary.LittleEndian.Uint16(img[22:24]))
	bootLabel := strings.TrimRight(string(img[43:54]), " ")
	fsType := strings.TrimRight(string(img[54:62]), " ")
	fatStart := reserved * bps
	rootStart := (reserved + nfats*fatSectors) * bps
	dataStart := rootStart + rootEntries*32
	clusterBytes := spc * bps
	next := func(c int) int { return int(binary.LittleEndian.Uint16(img[fatStart+2*c:])) }
	var label string
	var files []fatFile
	var lfn []uint16
	for i := 0; i < rootEntries; i++ {
		e := img[rootStart+32*i : rootStart+32*i+32]
		if e[0] == 0 {
			break
		}
		if e[0] == 0xE5 {
			continue
		}
		attr := e[11]
		if attr == 0x0F {
			var part []uint16
			for _, r := range [][2]int{{1, 11}, {14, 26}, {28, 32}} {
				for o := r[0]; o < r[1]; o += 2 {
					part = append(part, binary.LittleEndian.Uint16(e[o:]))
				}
			}
			lfn = append(part, lfn...)
			continue
		}
		if attr&0x08 != 0 {
			label = strings.TrimRight(string(e[0:11]), " ")
			lfn = nil
			continue
		}
		name := strings.TrimRight(string(e[0:8]), " ")
		if len(lfn) > 0 {
			end := len(lfn)
			for k, u := range lfn {
				if u == 0 {
					end = k
					break
				}
			}
			name = string(utf16.Decode(lfn[:end]))
		}
		lfn = nil
		size := int(binary.LittleEndian.Uint32(e[28:32]))
		cluster := int(binary.LittleEndian.Uint16(e[26:28]))
		var body []byte
		for cluster >= 2 && cluster < 0xFFF8 && len(body) < size {
			off := dataStart + (cluster-2)*clusterBytes
			body = append(body, img[off:off+clusterBytes]...)
			cluster = next(cluster)
		}
		if len(body) < size {
			t.Fatalf("%s: chain ends after %d of %d bytes", name, len(body), size)
		}
		files = append(files, fatFile{name: name, body: body[:size]})
	}
	if fsType != "FAT16" {
		t.Fatalf("boot sector names fs type %q", fsType)
	}
	return bootLabel, label, files
}

func TestTheCidataDriveIsAValidNoCloudSeed(t *testing.T) {
	seed := sampleSeed()
	path := filepath.Join(t.TempDir(), "cidata.img")
	if err := WriteFile(path, seed); err != nil {
		t.Fatal(err)
	}
	img, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	bootLabel, label, files := readFAT(t, img)
	if bootLabel != Label || label != Label {
		t.Fatalf("labels boot=%q root=%q, want %q in both", bootLabel, label, Label)
	}
	want := map[string][]byte{"user-data": seed.UserData, "meta-data": seed.MetaData, "network-config": seed.NetworkConfig}
	if len(files) != len(want) {
		t.Fatalf("root holds %d files, want %d", len(files), len(want))
	}
	for _, f := range files {
		body, ok := want[f.name]
		if !ok {
			t.Fatalf("unexpected file %q", f.name)
		}
		if !bytes.Equal(body, f.body) {
			t.Fatalf("%s differs from the document given", f.name)
		}
	}
	if fsck, err := exec.LookPath("fsck.fat"); err == nil {
		if out, err := exec.Command(fsck, "-n", "-v", path).CombinedOutput(); err != nil {
			t.Fatalf("fsck.fat -n: %v: %s", err, out)
		}
	}
	if blkid, err := exec.LookPath("blkid"); err == nil {
		out, err := exec.Command(blkid, "-p", "-o", "export", path).CombinedOutput()
		if err != nil {
			t.Fatalf("blkid: %v: %s", err, out)
		}
		if !strings.Contains(string(out), "LABEL="+Label+"\n") || !strings.Contains(string(out), "TYPE=vfat\n") {
			t.Fatalf("blkid does not see a vfat labelled %s: %s", Label, out)
		}
	}
	if mtype, err := exec.LookPath("mtype"); err == nil {
		for name, body := range want {
			cmd := exec.Command(mtype, "-i", path, "::"+name)
			cmd.Env = append(os.Environ(), "MTOOLS_SKIP_CHECK=1")
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("mtype ::%s: %v", name, err)
			}
			if !bytes.Equal(out, body) {
				t.Fatalf("mtype ::%s returned %d bytes, want %d", name, len(out), len(body))
			}
		}
	}
}

func TestTheSameDocumentsMakeAByteIdenticalDrive(t *testing.T) {
	a, err := Image(sampleSeed())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Image(sampleSeed())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("two drives of the same documents differ")
	}
	other := sampleSeed()
	other.MetaData = []byte("instance-id: fc-other\n")
	c, err := Image(other)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, c) {
		t.Fatal("different documents made the same drive")
	}
}

func TestASeedWithoutUserDataIsRefused(t *testing.T) {
	if _, err := Image(Seed{MetaData: []byte("instance-id: x\n")}); err == nil {
		t.Fatal("a seed with no user-data was written")
	}
	if _, err := Image(Seed{UserData: []byte("#cloud-config\n")}); err == nil {
		t.Fatal("a seed with no meta-data was written")
	}
}

func TestAnOmittedNetworkConfigIsNotWritten(t *testing.T) {
	seed := sampleSeed()
	seed.NetworkConfig = nil
	img, err := Image(seed)
	if err != nil {
		t.Fatal(err)
	}
	_, _, files := readFAT(t, img)
	for _, f := range files {
		if f.name == "network-config" {
			t.Fatal("an empty network-config was written as a file")
		}
	}
}
