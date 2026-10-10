package clusterstack

import (
	"path/filepath"
)

const (
	openBaoTLSDirName   = "tls"
	openBaoTLSCertName  = "server.crt"
	openBaoTLSKeyName   = "server.key"
	openBaoTLSCAName    = "ca.crt"
	openBaoTLSCAKeyName = "ca.key"
)

func OpenBaoTLSDir(dataDir string) string {
	return filepath.Join(OpenBaoDir(dataDir), openBaoTLSDirName)
}

func OpenBaoTLSCertPath(dataDir string) string {
	return filepath.Join(OpenBaoTLSDir(dataDir), openBaoTLSCertName)
}

func OpenBaoTLSKeyPath(dataDir string) string {
	return filepath.Join(OpenBaoTLSDir(dataDir), openBaoTLSKeyName)
}

func OpenBaoTLSCAPath(dataDir string) string {
	return filepath.Join(OpenBaoTLSDir(dataDir), openBaoTLSCAName)
}

func OpenBaoTLSCAKeyPath(dataDir string) string {
	return filepath.Join(OpenBaoTLSDir(dataDir), openBaoTLSCAKeyName)
}
