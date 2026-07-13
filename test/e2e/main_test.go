package e2e

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"testing"

	"k8s.io/klog/v2"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	"github.com/openmcp-project/openmcp-testing/pkg/providers"
	"github.com/openmcp-project/openmcp-testing/pkg/setup"
	"github.com/openmcp-project/openmcp-testing/pkg/setup/extensions"
	"github.com/openmcp-project/openmcp-testing/pkg/setup/extensions/fluxcd"
)

var testenv env.Environment

func TestMain(m *testing.M) {
	initLogging()
	version := mustVersion()
	openmcp := setup.OpenMCPSetup{
		Namespace: "openmcp-system",
		Operator: setup.OpenMCPOperatorSetup{
			Name: "openmcp-operator",
			// renovate: datasource=docker depName=ghcr.io/openmcp-project/images/openmcp-operator
			Image:        "ghcr.io/openmcp-project/images/openmcp-operator:v1.2.0",
			Environment:  "debug",
			PlatformName: "platform",
		},
		ClusterProviders: []providers.ClusterProviderSetup{
			{
				Name: "kind",
				// renovate: datasource=docker depName=ghcr.io/openmcp-project/images/cluster-provider-kind
				Image: "ghcr.io/openmcp-project/images/cluster-provider-kind:v0.5.0",
			},
		},
		ServiceProviders: []providers.ServiceProviderSetup{
			{
				Name:               "metricsoperator",
				Image:              fmt.Sprintf("ghcr.io/openmcp-project/images/service-provider-metrics-operator:%s", version),
				LoadImageToCluster: true,
			},
		},
		Extensions: []extensions.Extension{
			&fluxcd.FluxCD{},
		},
	}
	testenv = env.NewWithConfig(envconf.New().WithNamespace(openmcp.Namespace))
	platformCluster := openmcp.Bootstrap(testenv)
	handleSignals(platformCluster)
	os.Exit(testenv.Run(m))
}

// handleSignals intercepts SIGQUIT/SIGTERM/SIGINT so that when go test fires its
// timeout it sends SIGQUIT to the subprocess, which by default triggers a goroutine
// dump and os.Exit — bypassing testenv.Run's defer and leaking kind clusters.
// Deleting the platform kind cluster is enough: the cluster-provider sets finalizers
// on child clusters and cascades their deletion.
func handleSignals(platformCluster string) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		sig := <-sigs
		klog.Errorf("e2e suite received %s — deleting platform kind cluster %s and exiting", sig, platformCluster)
		if out, err := exec.Command("kind", "delete", "cluster", "--name", platformCluster).CombinedOutput(); err != nil {
			klog.Errorf("kind delete cluster %s: %v\n%s", platformCluster, err, out)
		}
		os.Exit(1)
	}()
}

func mustVersion() string {
	cmd := exec.Command("../../hack/common/get-version.sh")
	version, err := cmd.Output()
	if err != nil {
		panic(err)
	}
	return strings.TrimSpace(string(version))
}

func initLogging() {
	klog.InitFlags(nil)
	if err := flag.Set("v", "2"); err != nil {
		panic(err)
	}
	flag.Parse()
}
