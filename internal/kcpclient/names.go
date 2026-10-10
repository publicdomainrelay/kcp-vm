package kcpclient

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
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

const GrantOwnerPlaceholder = "__SOCIALWEB_OWNER_SERVICE_ACCOUNT__"

func RenderTenantGrant(doc, ownerDID string) (string, error) {
	if ownerDID == "" {
		return "", errors.New(
			"kcpclient: the tenant grant needs the owning DID: every name it scopes is derived from it, " +
				"and a grant rendered without one would be scoped to a name nothing owns")
	}
	if !strings.Contains(doc, GrantOwnerPlaceholder) {
		return "", fmt.Errorf(
			"kcpclient: the tenant grant carries no %s, so rendering it would apply a grant scoped to "+
				"whatever name is written in the file rather than to the cluster's owner",
			GrantOwnerPlaceholder)
	}
	return strings.ReplaceAll(doc, GrantOwnerPlaceholder, ServiceAccountName(ownerDID)), nil
}

const (
	AccountWorkspaceType kcpv1alpha1.WorkspaceTypeName = "account"
	HomeWorkspaceType    kcpv1alpha1.WorkspaceTypeName = "cluster"
)
