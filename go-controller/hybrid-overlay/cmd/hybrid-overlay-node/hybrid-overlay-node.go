package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/urfave/cli/v2"

	"github.com/ovn-org/ovn-kubernetes/go-controller/hybrid-overlay/pkg/controller"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/config"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/informer"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/kube"
	"github.com/ovn-org/ovn-kubernetes/go-controller/pkg/util"

	"k8s.io/client-go/informers"
	"k8s.io/klog"
	kexec "k8s.io/utils/exec"
)

var nodeName string
var runAsWindowsService bool

const appName = "hybrid-overlay-node"

func main() {
	c := cli.NewApp()
	c.Name = appName
	c.Usage = "a node controller to integrate disparate networks with VXLAN tunnels"
	c.Version = config.Version
	c.Flags = config.GetFlags([]cli.Flag{
		&cli.StringFlag{
			Name:        "node",
			Usage:       "The name of this node in the Kubernetes cluster.",
			Destination: &nodeName,
		},
		&cli.BoolFlag{
			Name:        "windows-service",
			Usage:       "Enables hybrid overlay to run as a Windows service. Ignored on Linux.",
			Destination: &runAsWindowsService,
		}})
	c.Action = func(c *cli.Context) error {
		if err := runHybridOverlay(c); err != nil {
			panic(err.Error())
		}
		return nil
	}

	ctx := context.Background()

	// trap SIGHUP, SIGINT, SIGTERM, SIGQUIT and
	// cancel the context
	ctx, cancel := context.WithCancel(ctx)
	exitCh := make(chan os.Signal, 1)
	signal.Notify(exitCh,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)
	defer func() {
		signal.Stop(exitCh)
		cancel()
	}()
	go func() {
		select {
		case s := <-exitCh:
			klog.Infof("Received signal %s. Shutting down", s)
			cancel()
		case <-ctx.Done():
		}
	}()

	if err := c.RunContext(ctx, os.Args); err != nil {
		klog.Exit(err)
	}
}

func runHybridOverlay(ctx *cli.Context) error {
	exec := kexec.New()
	if _, err := config.InitConfig(ctx, exec, nil); err != nil {
		return err
	}

	if err := util.SetExecWithoutOVS(exec); err != nil {
		return err
	}

	if nodeName == "" {
		return fmt.Errorf("missing node name; use the 'node' flag to provide one")
	}

	if err := initForOS(runAsWindowsService); err != nil {
		klog.Infof("Error initializing Windows service: %v", err)
	}

	clientset, _, _, _, err := util.NewClientsets(&config.Kubernetes)
	if err != nil {
		return err
	}

	stopChan := make(chan struct{})
	defer close(stopChan)
	f := informers.NewSharedInformerFactory(clientset, informer.DefaultResyncInterval)

	n, err := controller.NewNode(
		&kube.Kube{KClient: clientset},
		nodeName,
		f.Core().V1().Nodes().Informer(),
		f.Core().V1().Pods().Informer(),
	)
	if err != nil {
		return err
	}

	f.Start(stopChan)
	go func() {
		err := n.Run(stopChan)
		if err != nil {
			klog.Error(err)
		}
	}()

	// run until cancelled
	<-ctx.Context.Done()
	close(stopChan)
	return nil
}
