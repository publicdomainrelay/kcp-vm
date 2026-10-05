package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/johnandersen777/socialweb-computer-kcp/internal/cloudprovider"
)

var logger = slog.New(slog.NewJSONHandler(os.Stderr, nil))

func log(event string, fields map[string]any) {
	attrs := make([]any, 0, len(fields)*2)
	for k, v := range fields {
		attrs = append(attrs, k, v)
	}
	logger.Info(event, attrs...)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "socialweb-cloud-controller-manager:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	restCfg, err := clientcmd.BuildConfigFromFlags("", cfg.TenantKubeconfig)
	if err != nil {
		return fmt.Errorf(
			"socialweb-cloud-controller-manager: reading the minted tenant kubeconfig at %s: %w",
			cfg.TenantKubeconfig, err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf(
			"socialweb-cloud-controller-manager: building the client for the tenant cluster %s "+
				"from %s: %w", cfg.Cluster, cfg.TenantKubeconfig, err)
	}

	controller, err := cloudprovider.New(cloudprovider.Options{
		Cluster:   cfg.Cluster,
		Instances: cloudprovider.NewFileInstances(cfg.InstancesFile),
		Nodes:     cloudprovider.NewClientNodeWriter(client),
		Log:       log,
		Resync:    cfg.Resync,
	}, client)
	if err != nil {
		return err
	}

	log("cloud controller manager: status", map[string]any{
		"phase":         "Starting",
		"cluster":       cfg.Cluster,
		"tenant":        cfg.TenantKubeconfig,
		"instancesFile": cfg.InstancesFile,
		"maxNodes":      cfg.MaxNodes,
		"podsPerNode":   cfg.PodsPerNode,
		"resync":        cfg.Resync.String(),
	})

	if err := controller.Run(ctx); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}
