package guestdisk

import (
	"fmt"
	"os"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/guestseed"
)

func Clone(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("guestdisk: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("guestdisk: %w", err)
	}
	if err := cloneInto(out, in); err != nil {
		out.Close()
		os.Remove(dst)
		return fmt.Errorf("guestdisk: copying %s to %s: %w", src, dst, err)
	}
	return out.Close()
}

func GrowTo(path string, mib int) error {
	if mib <= 0 {
		return nil
	}
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("guestdisk: %w", err)
	}
	want := int64(mib) << 20
	if st.Size() >= want {
		return nil
	}
	if err := os.Truncate(path, want); err != nil {
		return fmt.Errorf("guestdisk: growing %s to %d MiB: %w", path, mib, err)
	}
	return nil
}

func WriteSeed(path string, userData, metaData, networkConfig []byte) error {
	return guestseed.WriteFile(path, guestseed.Seed{UserData: userData, MetaData: metaData, NetworkConfig: networkConfig})
}
