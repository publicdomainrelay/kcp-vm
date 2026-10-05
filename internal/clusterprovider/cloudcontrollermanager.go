package clusterprovider

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/clusterstack"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/processrunner"
	"github.com/johnandersen777/socialweb-computer-kcp/internal/procgroup"
)

const CloudControllerManagerName = "system:socialweb:cloud-controller-manager"

const CloudControllerManagerGroup = "system:socialweb:cloud-controller-manager"

const CloudControllerManagerClusterRole = "socialweb-cloud-controller-manager"

const CloudControllerManagerClusterRoleBinding = "socialweb-cloud-controller-manager"

const cloudControllerManagerKubeconfigContext = "socialweb-cloud-controller-manager"

const cloudControllerManagerKubeconfigMode = 0o600

var ErrNoCloudControllerManagerSeam = errors.New(
	"clusterprovider: this Stack puts no k3s step in the group, so the cloud controller manager's " +
		"minted credential has no moment after the cluster's CAs exist to be written in, and the " +
		"controller would start against a kubeconfig that does not exist -- which the process would " +
		"report as a missing file, not as a missing seam")

var ErrNoCloudControllerManagerCredential = errors.New(
	"clusterprovider: the cloud controller manager needs a credential minted for it and no admin " +
		"client was supplied to grant that identity its RBAC. Invariant 20 forbids the controller " +
		"the k3s admin kubeconfig, and a scoped identity with no binding is authenticated and denied, " +
		"so the controller would come up and never sync its informer")

var ErrCloudControllerManagerKubeconfigUnwritable = errors.New(
	"clusterprovider: the cloud controller manager's minted tenant kubeconfig could not be put on " +
		"disk before its step ran. The controller takes that file as its only credential and refuses " +
		"to start without it, so a write that failed quietly would leave the component failing at " +
		"start for a reason that is not in the controller's own log")

func WriteCloudControllerManagerKubeconfig(path string, cfg *rest.Config) error {
	if cfg == nil {
		return fmt.Errorf("%w: no credential was rendered for %s", ErrCloudControllerManagerKubeconfigUnwritable, path)
	}
	if path == "" {
		return fmt.Errorf("%w: the target path is empty", ErrCloudControllerManagerKubeconfigUnwritable)
	}
	body, err := clientcmd.Write(clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{cloudControllerManagerKubeconfigContext: {
			Server:                   cfg.Host,
			CertificateAuthorityData: cfg.CAData,
			InsecureSkipTLSVerify:    cfg.Insecure,
		}},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{cloudControllerManagerKubeconfigContext: {
			ClientCertificateData: cfg.CertData,
			ClientKeyData:         cfg.KeyData,
			Token:                 cfg.BearerToken,
		}},
		Contexts: map[string]*clientcmdapi.Context{cloudControllerManagerKubeconfigContext: {
			Cluster:  cloudControllerManagerKubeconfigContext,
			AuthInfo: cloudControllerManagerKubeconfigContext,
		}},
		CurrentContext: cloudControllerManagerKubeconfigContext,
	})
	if err != nil {
		return fmt.Errorf("%w: rendering the credential for %s: %v",
			ErrCloudControllerManagerKubeconfigUnwritable, path, err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%w: the directory %s holding %s: %v",
			ErrCloudControllerManagerKubeconfigUnwritable, dir, path, err)
	}
	if info, err := os.Stat(path); err == nil {
		current, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("%w: reading %s: %v", ErrCloudControllerManagerKubeconfigUnwritable, path, err)
		}
		if string(current) == string(body) && info.Mode().Perm() == cloudControllerManagerKubeconfigMode {
			return nil
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: stat %s: %v", ErrCloudControllerManagerKubeconfigUnwritable, path, err)
	}

	tmp, err := os.CreateTemp(dir, clusterstack.CloudControllerManagerKubeconfigName+".")
	if err != nil {
		return fmt.Errorf("%w: a temporary file beside %s: %v",
			ErrCloudControllerManagerKubeconfigUnwritable, path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := tmp.Chmod(cloudControllerManagerKubeconfigMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: the mode of %s: %v", ErrCloudControllerManagerKubeconfigUnwritable, tmp.Name(), err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("%w: writing %s: %v", ErrCloudControllerManagerKubeconfigUnwritable, tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%w: closing %s: %v", ErrCloudControllerManagerKubeconfigUnwritable, tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("%w: renaming %s over %s: %v",
			ErrCloudControllerManagerKubeconfigUnwritable, tmp.Name(), path, err)
	}
	return nil
}

func GrantCloudControllerManager(ctx context.Context, core kubernetes.Interface, cluster string) error {
	if core == nil {
		return fmt.Errorf("%w (cluster %s)", ErrNoCloudControllerManagerCredential, cluster)
	}
	role := cloudControllerManagerClusterRole()
	if _, err := core.RbacV1().ClusterRoles().Create(ctx, role, metav1.CreateOptions{FieldManager: fieldManager}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("clusterprovider: creating the RBAC ClusterRole %s: %w", role.Name, err)
		}
		if _, err := core.RbacV1().ClusterRoles().Update(ctx, role, metav1.UpdateOptions{FieldManager: fieldManager}); err != nil {
			return fmt.Errorf("clusterprovider: updating the RBAC ClusterRole %s: %w", role.Name, err)
		}
	}
	binding := cloudControllerManagerClusterRoleBinding()
	if _, err := core.RbacV1().ClusterRoleBindings().Create(ctx, binding, metav1.CreateOptions{FieldManager: fieldManager}); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("clusterprovider: creating the RBAC ClusterRoleBinding %s: %w", binding.Name, err)
		}
		if _, err := core.RbacV1().ClusterRoleBindings().Update(ctx, binding, metav1.UpdateOptions{FieldManager: fieldManager}); err != nil {
			return fmt.Errorf("clusterprovider: updating the RBAC ClusterRoleBinding %s: %w", binding.Name, err)
		}
	}
	return nil
}

func cloudControllerManagerClusterRole() *rbacv1.ClusterRole {
	return &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: CloudControllerManagerClusterRole},
		Rules: []rbacv1.PolicyRule{{
			APIGroups: []string{""},
			Resources: []string{"nodes"},
			Verbs:     []string{"get", "list", "watch", "patch", "update", "delete"},
		}},
	}
}

func cloudControllerManagerClusterRoleBinding() *rbacv1.ClusterRoleBinding {
	return &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: CloudControllerManagerClusterRoleBinding},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     CloudControllerManagerClusterRole,
		},
		Subjects: []rbacv1.Subject{{
			Kind:     rbacv1.GroupKind,
			APIGroup: rbacv1.GroupName,
			Name:     CloudControllerManagerGroup,
		}},
	}
}

func prepareCloudControllerManager(steps []procgroup.Step, cfg StackConfig, cluster, apiserver string) ([]procgroup.Step, error) {
	idx := -1
	for i, s := range steps {
		if s.Name == procgroup.ComponentK3s {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, ErrNoCloudControllerManagerSeam
	}
	if cfg.Admin == nil {
		return nil, ErrNoCloudControllerManagerCredential
	}
	dataDir, err := ClusterDataDir(cfg.StateDir, cluster)
	if err != nil {
		return nil, err
	}
	kubeconfig := clusterstack.CloudControllerManagerKubeconfig(dataDir)
	inner := steps[idx].Ready
	steps[idx].Ready = func(ctx context.Context, p *processrunner.Process) error {
		if inner != nil {
			if err := inner(ctx, p); err != nil {
				return err
			}
		}
		minted, err := MintTenantCredentialForClusterAs(ctx, cfg.StateDir, cluster, apiserver,
			CloudControllerManagerName, []string{CloudControllerManagerGroup}, DefaultCredentialValidity)
		if err != nil {
			return fmt.Errorf("clusterprovider: minting the scoped credential the cloud controller "+
				"manager of %s authenticates with: %w", cluster, err)
		}
		if err := WriteCloudControllerManagerKubeconfig(kubeconfig, minted); err != nil {
			return err
		}
		core, err := cfg.Admin(ctx, cluster, apiserver)
		if err != nil {
			return err
		}
		return GrantCloudControllerManager(ctx, core, cluster)
	}
	return steps, nil
}
