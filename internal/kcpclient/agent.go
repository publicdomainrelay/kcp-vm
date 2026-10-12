package kcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
)

const (
	NodeClaimAgentGrant  = "socialweb-nodeclaim-agent"
	NodeClaimHomeLabel   = "socialweb.computer/nodeclaim-home"
	NodeClaimHomeKey     = "workspace"
	foreignAccountPrefix = "system:kcp:serviceaccount:"
	agentFieldManager    = "kcpclient"
)

type ServiceAccountRef struct {
	Workspace string
	Namespace string
	Name      string
}

func ParseServiceAccountRef(s string) (*ServiceAccountRef, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return nil, fmt.Errorf("kcpclient: %q is not <workspace path>/<namespace>/<name>", s)
	}
	return &ServiceAccountRef{Workspace: parts[0], Namespace: parts[1], Name: parts[2]}, nil
}

func HomeAgentRules() []rbacv1.PolicyRule {
	return []rbacv1.PolicyRule{
		{NonResourceURLs: []string{"/"}, Verbs: []string{"access"}},
		{APIGroups: []string{socialwebResource.Group}, Resources: []string{"nodeclaims"}, Verbs: []string{"get", "list", "patch"}},
		{APIGroups: []string{socialwebResource.Group}, Resources: []string{"nodeclaims/status"}, Verbs: []string{"patch"}},
		{APIGroups: []string{socialwebResource.Group}, Resources: []string{"clusterlinks"}, Verbs: []string{"get"}},
	}
}

type agent struct {
	ref  *ServiceAccountRef
	mu   sync.Mutex
	user string
}

func (a *agent) userName(ctx context.Context, c *Client) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.user != "" {
		return a.user, nil
	}
	i := strings.LastIndex(a.ref.Workspace, ":")
	if i < 0 {
		return "", fmt.Errorf("kcpclient: the node claim agent's workspace %q has no parent", a.ref.Workspace)
	}
	ws, err := c.readWorkspace(ctx, a.ref.Workspace[:i], a.ref.Workspace[i+1:])
	if err != nil {
		return "", fmt.Errorf("kcpclient: the node claim agent's workspace %s: %w", a.ref.Workspace, err)
	}
	if ws.Spec.Cluster == "" {
		return "", fmt.Errorf("kcpclient: the node claim agent's workspace %s has no logical cluster yet", a.ref.Workspace)
	}
	a.user = foreignAccountPrefix + ws.Spec.Cluster + ":" + a.ref.Namespace + ":" + a.ref.Name
	return a.user, nil
}

func apply(ctx context.Context, req *rest.Request, where string, obj any) error {
	body, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	if err := req.Param("fieldManager", agentFieldManager).Param("force", "true").Body(body).Do(ctx).Error(); err != nil {
		return fmt.Errorf("kcpclient: the node claim agent's grant in %s: %w", where, err)
	}
	return nil
}

func (c *Client) grantAgentHome(ctx context.Context, home string) error {
	if c.agent == nil {
		return nil
	}
	user, err := c.agent.userName(ctx, c)
	if err != nil {
		return err
	}
	r, err := c.bind(home, rbacv1.SchemeGroupVersion, "/apis")
	if err != nil {
		return err
	}
	role := rbacv1.ClusterRole{TypeMeta: metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRole"},
		ObjectMeta: metav1.ObjectMeta{Name: NodeClaimAgentGrant}, Rules: HomeAgentRules()}
	if err := apply(ctx, r.Patch(types.ApplyPatchType).Resource("clusterroles").Name(NodeClaimAgentGrant), home, role); err != nil {
		return err
	}
	binding := rbacv1.ClusterRoleBinding{TypeMeta: metav1.TypeMeta{APIVersion: rbacv1.SchemeGroupVersion.String(), Kind: "ClusterRoleBinding"},
		ObjectMeta: metav1.ObjectMeta{Name: NodeClaimAgentGrant},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: NodeClaimAgentGrant},
		Subjects:   []rbacv1.Subject{{Kind: rbacv1.UserKind, APIGroup: rbacv1.GroupName, Name: user}}}
	if err := apply(ctx, r.Patch(types.ApplyPatchType).Resource("clusterrolebindings").Name(NodeClaimAgentGrant), home, binding); err != nil {
		return err
	}
	key, err := ClusterKey(home)
	if err != nil {
		return err
	}
	core, err := c.coreResource(c.agent.ref.Workspace)
	if err != nil {
		return err
	}
	index := corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: key, Namespace: c.agent.ref.Namespace, Labels: map[string]string{NodeClaimHomeLabel: "true"}},
		Data:       map[string]string{NodeClaimHomeKey: home}}
	return apply(ctx, core.Patch(types.ApplyPatchType).Namespace(c.agent.ref.Namespace).Resource("configmaps").Name(key),
		c.agent.ref.Workspace, index)
}
