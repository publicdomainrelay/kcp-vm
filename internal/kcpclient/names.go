package kcpclient

import (
	"crypto/sha256"
	"encoding/base32"
	"math/big"
	"strings"

	kcpv1alpha1 "github.com/kcp-dev/sdk/apis/tenancy/v1alpha1"
)

const (
	RootWorkspace = "root"

	ChildLabel = "clusters"
)

func AccountHash(did string) string {
	sum := sha256.Sum256([]byte(did))
	return new(big.Int).SetBytes(sum[:]).Text(36)[:8]
}

func AccountPath(did string) string {
	return RootWorkspace + ":" + AccountHash(did)
}

func HomePath(did, cluster string) string {
	return AccountPath(did) + ":" + cluster
}

func MountPath(did, cluster string) string {
	return HomePath(did, cluster) + ":" + ChildLabel
}

func IdentityRecordName(did string) string {
	return AccountHash(did)
}

const TenantNamespace = "users"

func ServiceAccountName(did string) string {
	return "did-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte(did)))
}

const (
	AccountWorkspaceType kcpv1alpha1.WorkspaceTypeName = "account"
	HomeWorkspaceType    kcpv1alpha1.WorkspaceTypeName = "cluster"
)
