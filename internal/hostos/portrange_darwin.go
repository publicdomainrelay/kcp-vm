//go:build darwin

package hostos

func LocalPortRange() (int, int, error) {
	return 0, 0, unsupported("reading the local port range", "0012")
}
