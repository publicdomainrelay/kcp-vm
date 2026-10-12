package marketnetwork

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestTwoNetworksTakeDifferentSlots(t *testing.T) {
	dir := t.TempDir()
	a, err := TakeSlot(dir, 4, "b3d00001")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	b, err := TakeSlot(dir, 4, "b3d00002")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Release()
	if a.Index == b.Index {
		t.Fatalf("both market networks hold slot %d", a.Index)
	}
	body, err := os.ReadFile(a.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "b3d00001") {
		t.Fatalf("slot file %s reads %q; it names the market network holding it for whoever is diagnosing a full host", a.Path, body)
	}
}

func TestAFullHostRefusesAnotherNetwork(t *testing.T) {
	dir := t.TempDir()
	var held []*Slot
	for i := 0; i < 2; i++ {
		s, err := TakeSlot(dir, 2, "b3d0000"+string(rune('1'+i)))
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, s)
	}
	if _, err := TakeSlot(dir, 2, "b3d00003"); !errors.Is(err, ErrNoFreeSlot) {
		t.Fatalf("err = %v, want ErrNoFreeSlot with every slot held", err)
	}
	if err := held[0].Release(); err != nil {
		t.Fatal(err)
	}
	again, err := TakeSlot(dir, 2, "b3d00003")
	if err != nil {
		t.Fatalf("a released slot is not taken again: %v", err)
	}
	again.Release()
	held[1].Release()
}

func TestTheMemoryGuardRefusesBelowTheNetworksBudget(t *testing.T) {
	meminfo := "MemTotal:       48000000 kB\nMemFree:         1000000 kB\nMemAvailable:    8000000 kB\n"
	avail, err := MemAvailableKiB(meminfo)
	if err != nil || avail != 8000000 {
		t.Fatalf("MemAvailable = %d, %v", avail, err)
	}
	if got, want := BudgetKiB(2), uint64(3*1024*1024+2*2560*1024); got != want {
		t.Fatalf("budget for 2 guests = %d KiB, want %d (3 GiB + 2.5 GiB per guest)", got, want)
	}
	if err := CheckMemory(meminfo, 1); err != nil {
		t.Fatalf("8 GB available is refused for one guest: %v", err)
	}
	if err := CheckMemory(meminfo, 2); err == nil {
		t.Fatal("8 GB available is accepted for two guests, whose budget is 8 GiB")
	}
	if err := CheckMemory("MemTotal: 1 kB\n", 1); err == nil {
		t.Fatal("a meminfo with no MemAvailable is accepted")
	}
}
