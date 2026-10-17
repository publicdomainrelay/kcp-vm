//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const probeImage = "docker.io/library/busybox:1.36.1@sha256:73aaf090f3d85aa34ee199857f03fa3a95c8ede2ffd4cc2cdb5b94e566b11662"

func openTenantKube(gatewayDir, name string) (*kubernetes.Clientset, error) {
	dirs, _ := filepath.Glob(filepath.Join(gatewayDir, "clusterprovider", "pdr-*-"+name))
	if len(dirs) != 1 {
		return nil, fmt.Errorf("no single cluster dir for %s under %s/clusterprovider: %v", name, gatewayDir, dirs)
	}
	var front struct {
		Address string `json:"address"`
	}
	b, err := os.ReadFile(filepath.Join(dirs[0], "front.json"))
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &front); err != nil || front.Address == "" {
		return nil, fmt.Errorf("front.json of %s: %v", name, err)
	}
	tls := filepath.Join(dirs[0], "server", "tls")
	ca, err := os.ReadFile(filepath.Join(tls, "server-ca.crt"))
	if err != nil {
		return nil, err
	}
	cert, err := os.ReadFile(filepath.Join(tls, "client-admin.crt"))
	if err != nil {
		return nil, err
	}
	key, err := os.ReadFile(filepath.Join(tls, "client-admin.key"))
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(&rest.Config{Host: "https://" + front.Address, Timeout: 20 * time.Second,
		TLSClientConfig: rest.TLSClientConfig{CAData: ca, CertData: cert, KeyData: key}})
}

type tenantCounters struct {
	cycles, scaledOut, scaledIn, failed int64
}

func tenantCycle(ctx context.Context, kube *kubernetes.Clientset, seq int, hold time.Duration, out, in time.Duration) (scaledOut, scaledIn bool, err error) {
	pods := kube.CoreV1().Pods("default")
	name := fmt.Sprintf("soak-%d-%d", time.Now().Unix(), seq)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: corev1.PodSpec{
			TerminationGracePeriodSeconds: new(int64),
			Containers: []corev1.Container{{
				Name: "probe", Image: probeImage, Command: []string{"sh", "-c", "sleep 3600"},
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("10m"), corev1.ResourceMemory: resource.MustParse("16Mi")}},
			}},
		},
	}
	for deadline := time.Now().Add(time.Minute); ; {
		if _, e := kube.CoreV1().ServiceAccounts("default").Get(ctx, "default", metav1.GetOptions{}); e == nil {
			break
		}
		if time.Now().After(deadline) || !sleepCtx(ctx, time.Second) {
			return false, false, fmt.Errorf("the default ServiceAccount of default did not appear")
		}
	}
	if _, err = pods.Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return false, false, err
	}
	defer pods.Delete(context.WithoutCancel(ctx), name, metav1.DeleteOptions{GracePeriodSeconds: new(int64)})
	for deadline := time.Now().Add(out); ; {
		p, e := pods.Get(ctx, name, metav1.GetOptions{})
		if e == nil && p.Status.Phase == corev1.PodRunning {
			scaledOut = true
			break
		}
		if time.Now().After(deadline) {
			return false, false, fmt.Errorf("pod %s not Running within %s", name, out)
		}
		if !sleepCtx(ctx, 2*time.Second) {
			return false, false, ctx.Err()
		}
	}
	sleepCtx(ctx, hold)
	if err = pods.Delete(ctx, name, metav1.DeleteOptions{GracePeriodSeconds: new(int64)}); err != nil {
		return scaledOut, false, err
	}
	for deadline := time.Now().Add(in); ; {
		nodes, e := kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
		if e == nil && len(nodes.Items) == 0 {
			return scaledOut, true, nil
		}
		if time.Now().After(deadline) {
			names := []string{}
			if e == nil {
				for _, n := range nodes.Items {
					names = append(names, n.Name)
				}
			}
			return scaledOut, false, fmt.Errorf("nodes still present %s after %s", strings.Join(names, ","), in)
		}
		if !sleepCtx(ctx, 3*time.Second) {
			return scaledOut, false, ctx.Err()
		}
	}
}
