package e2e

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"k8s.io/klog/v2"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/pkg/env"
	"sigs.k8s.io/e2e-framework/pkg/envconf"

	"github.com/openmcp-project/openmcp-testing/pkg/clusterutils"
	"github.com/openmcp-project/openmcp-testing/pkg/providers"
	"github.com/openmcp-project/openmcp-testing/pkg/setup"
	"github.com/openmcp-project/openmcp-testing/pkg/setup/extensions"
	"github.com/openmcp-project/openmcp-testing/pkg/setup/extensions/fluxcd"
	localsetup "github.com/openmcp-project/service-provider-metrics-operator/test/e2e/setup"
)

var testenv env.Environment

func TestMain(m *testing.M) {
	initLogging()
	version := mustVersion()
	k0smotronExt := &localsetup.K0smotronSetup{}
	openmcp := setup.OpenMCPSetup{
		Namespace: "openmcp-system",
		WaitOpts:  []wait.Option{wait.WithTimeout(5 * time.Minute)},
		Operator: setup.OpenMCPOperatorSetup{
			Name: "openmcp-operator",
			// renovate: datasource=docker depName=ghcr.io/openmcp-project/images/openmcp-operator
			Image:       "ghcr.io/openmcp-project/images/openmcp-operator:v1.2.0",
			Environment: "debug",
			// Use k0smotron profile for non-platform cluster purposes
			ExtraClusterPurposeMapping: []providers.ClusterPurposeMapping{
				{Purpose: "mcp", Profile: "k0smotron", Tenancy: "Exclusive"},
				{Purpose: "onboarding", Profile: "k0smotron", Tenancy: "Shared"},
				{Purpose: "workload", Profile: "k0smotron", Tenancy: "Shared"},
			},
		},
		ClusterProviders: []providers.ClusterProviderSetup{
			{
				Name: "k0smotron",
				// renovate: datasource=docker depName=ghcr.io/openmcp-project/images/cluster-provider-k0smotron
				Image: "ghcr.io/openmcp-project/images/cluster-provider-k0smotron:v0.2.0",
			},
		},
		ServiceProviders: []providers.ServiceProviderSetup{
			{
				Name:               "metricsoperator",
				Image:              fmt.Sprintf("ghcr.io/openmcp-project/images/service-provider-metrics-operator:%s", version),
				LoadImageToCluster: true,
				WaitOpts:           []wait.Option{wait.WithTimeout(5 * time.Minute)},
			},
		},
		Extensions: []extensions.Extension{
			k0smotronExt,
			&fluxcd.FluxCD{},
		},
	}
	testenv = env.NewWithConfig(envconf.New().WithNamespace(openmcp.Namespace))
	openmcp.Bootstrap(testenv)

	var watchdogCancel context.CancelFunc
	// Register cluster provider and start AR watchdog after Bootstrap Setup funcs run.
	testenv.Setup(func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		clusterutils.SetClusterProvider(&localsetup.K0smotronClusterProvider{
			PlatformKubeconfig: k0smotronExt.PlatformKubeconfig,
			RestConfig:         cfg.Client().RESTConfig(),
		})
		// Replace the install-phase watchdog with one tied to a cancel we control.
		if k0smotronExt.StopWatchdog != nil {
			k0smotronExt.StopWatchdog()
		}
		wctx, cancel := context.WithCancel(context.Background())
		watchdogCancel = cancel
		localsetup.StartARWatchdog(wctx, cfg.Client().RESTConfig())
		return ctx, nil
	})
	testenv.Finish(func(ctx context.Context, cfg *envconf.Config) (context.Context, error) {
		if watchdogCancel != nil {
			watchdogCancel()
		}
		return ctx, nil
	})
	os.Exit(testenv.Run(m))
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
