package kcpclient

import (
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"fmt"
	"math/big"
	"regexp"
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

var clusterKeyShape = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func ClusterKey(home string) (string, error) {
	parts := strings.Split(home, ":")
	if len(parts) != 3 || parts[0] != RootWorkspace || parts[1] == "" || parts[2] == "" {
		return "", fmt.Errorf(
			"kcpclient: %q is not a home workspace path of the form %s:<account>:<cluster>, so the "+
				"key a cluster is known by inside a provider cannot be derived from it",
			home, RootWorkspace)
	}
	key := parts[1] + "-" + parts[2]
	if !clusterKeyShape.MatchString(key) {
		return "", fmt.Errorf(
			"kcpclient: the home workspace path %q resolves to %q, which is not a DNS-1123 label: "+
				"the key names a state dir, a port lease, a node and a mint credential, so a "+
				"character outside [a-z0-9-] would make it name nothing those can hold", home, key)
	}
	return key, nil
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
