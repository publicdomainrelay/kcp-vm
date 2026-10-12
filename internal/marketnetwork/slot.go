package marketnetwork

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const DefaultSlotDir = "/var/tmp/socialweb-market-network-slots"

const DefaultSlots = 4

const BaseBudgetKiB = 3 * 1024 * 1024

const GuestBudgetKiB = 2560 * 1024

var ErrNoFreeSlot = errors.New("every market network slot on this host is held")

type Slot struct {
	Index int
	Path  string
	file  *os.File
}

func TakeSlot(dir string, slots int, networkID string) (*Slot, error) {
	if slots <= 0 {
		slots = DefaultSlots
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return nil, err
	}
	_ = os.Chmod(dir, 0o777)
	start := os.Getpid() % slots
	for n := 0; n < slots; n++ {
		i := (start + n) % slots
		path := filepath.Join(dir, strconv.Itoa(i)+".lock")
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o666)
		if err != nil {
			return nil, err
		}
		busy, err := tryLock(f)
		if err != nil || busy {
			f.Close()
			if busy {
				continue
			}
			return nil, err
		}
		_ = f.Truncate(0)
		_, _ = f.WriteAt([]byte(fmt.Sprintf("%s %d %s\n", networkID, os.Getpid(), time.Now().UTC().Format(time.RFC3339))), 0)
		return &Slot{Index: i, Path: path, file: f}, nil
	}
	return nil, fmt.Errorf("%w (%d slots under %s)", ErrNoFreeSlot, slots, dir)
}

func (s *Slot) Release() error {
	if s == nil || s.file == nil {
		return nil
	}
	_ = s.file.Truncate(0)
	err := s.file.Close()
	s.file = nil
	return err
}

func BudgetKiB(guests int) uint64 {
	if guests < 0 {
		guests = 0
	}
	return BaseBudgetKiB + uint64(guests)*GuestBudgetKiB
}

func MemAvailableKiB(meminfo string) (uint64, error) {
	for _, line := range strings.Split(meminfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemAvailable:" {
			return strconv.ParseUint(fields[1], 10, 64)
		}
	}
	return 0, errors.New("meminfo carries no MemAvailable")
}

func CheckMemory(meminfo string, guests int) error {
	avail, err := MemAvailableKiB(meminfo)
	if err != nil {
		return err
	}
	if need := BudgetKiB(guests); avail < need {
		return fmt.Errorf("MemAvailable is %d MiB and a market network with %d guest(s) needs %d MiB (3 GiB + 2.5 GiB per guest)",
			avail/1024, guests, need/1024)
	}
	return nil
}
