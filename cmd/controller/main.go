package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"sa-imagepullsecret-controller/pkg/controller"
)

func main() {
	config, err := rest.InClusterConfig()
	if err != nil {
		klog.Fatalf("Error getting cluster config: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("Error creating clientset: %v", err)
	}

	ctrl := controller.NewController(clientset)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handling
	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-stopCh
		klog.Info("Received termination signal, shutting down")
		cancel()
	}()

	klog.Info("Starting controller")
	ctrl.Run(ctx)
	klog.Info("Controller stopped")
}
