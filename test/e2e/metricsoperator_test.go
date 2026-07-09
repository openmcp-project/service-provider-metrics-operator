package e2e

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/e2e-framework/klient/wait"
	"sigs.k8s.io/e2e-framework/klient/wait/conditions"
	"sigs.k8s.io/e2e-framework/pkg/envconf"
	"sigs.k8s.io/e2e-framework/pkg/features"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	libutils "github.com/openmcp-project/openmcp-operator/lib/utils"
	"github.com/openmcp-project/openmcp-testing/pkg/clusterutils"
	openmcpconditions "github.com/openmcp-project/openmcp-testing/pkg/conditions"
	"github.com/openmcp-project/openmcp-testing/pkg/providers"
	"github.com/openmcp-project/openmcp-testing/pkg/resources"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator"
)

const testMCP = "test-mcp"

func TestServiceProvider(t *testing.T) {
	var onboardingList unstructured.UnstructuredList
	basicProviderTest := features.New("metrics-operator provider test").
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			if _, err := resources.CreateObjectsFromDir(ctx, c, "platform"); err != nil {
				t.Errorf("failed to create platform cluster objects: %v", err)
			}
			return ctx
		}).
		Setup(providers.CreateMCP(testMCP)).
		Assess("create MetricsOperator and verify Ready",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				onboardingConfig, err := clusterutils.OnboardingConfig()
				if err != nil {
					t.Error(err)
					return ctx
				}
				objList, err := resources.CreateObjectsFromDir(ctx, onboardingConfig, "onboarding")
				if err != nil {
					t.Errorf("failed to create onboarding cluster objects: %v", err)
					return ctx
				}
				for _, obj := range objList.Items {
					if err := wait.For(openmcpconditions.Match(&obj, onboardingConfig, "Ready", corev1.ConditionTrue),
						wait.WithTimeout(10*time.Minute)); err != nil {
						t.Error(err)
					}
				}
				objList.DeepCopyInto(&onboardingList)
				return ctx
			},
		).
		Assess("platform cluster: OCIRepository and HelmRelease are ready",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				tenantNamespace, err := libutils.StableMCPNamespace(testMCP, "default")
				if err != nil {
					t.Errorf("failed to get tenant namespace: %v", err)
					return ctx
				}

				ociRepo := &sourcev1.OCIRepository{}
				ociRepo.SetName(metricsoperator.OCIRepositoryName)
				ociRepo.SetNamespace(tenantNamespace)
				if err := wait.For(openmcpconditions.Match(ociRepo, c, "Ready", corev1.ConditionTrue),
					wait.WithTimeout(5*time.Minute)); err != nil {
					t.Errorf("OCIRepository not ready: %v", err)
				}

				helmRelease := &helmv2.HelmRelease{}
				helmRelease.SetName(metricsoperator.HelmReleaseName)
				helmRelease.SetNamespace(tenantNamespace)
				if err := wait.For(openmcpconditions.Match(helmRelease, c, "Ready", corev1.ConditionTrue),
					wait.WithTimeout(5*time.Minute)); err != nil {
					t.Errorf("HelmRelease not ready: %v", err)
				}
				return ctx
			},
		).
		Assess("workload cluster: metrics-operator deployment exists",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				workloadConfig, err := clusterutils.ConfigByPrefix("workload", corev1.NamespaceDefault)
				if err != nil {
					t.Error(err)
					return ctx
				}
				dep := &appsv1.DeploymentList{}
				if err := wait.For(conditions.New(workloadConfig.Client().Resources(metricsoperator.DefaultNamespace)).
					ResourceListN(dep, 1),
					wait.WithTimeout(5*time.Minute)); err != nil {
					t.Errorf("metrics-operator deployment not found in namespace %s: %v", metricsoperator.DefaultNamespace, err)
				}
				return ctx
			},
		).
		Teardown(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			onboardingConfig, err := clusterutils.OnboardingConfig()
			if err != nil {
				t.Error(err)
				return ctx
			}
			for _, obj := range onboardingList.Items {
				if err := resources.DeleteObject(ctx, onboardingConfig, &obj, wait.WithTimeout(2*time.Minute)); err != nil {
					t.Errorf("failed to delete onboarding object: %v", err)
				}
			}
			return ctx
		}).
		Teardown(providers.DeleteMCP(testMCP, wait.WithTimeout(5*time.Minute)))
	testenv.Test(t, basicProviderTest.Feature())
}
