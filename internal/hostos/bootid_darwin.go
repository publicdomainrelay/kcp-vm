//go:build darwin

package hostos

func BootID() (string, error) {
	return "", unsupported("reading the boot id", "0012")
}
