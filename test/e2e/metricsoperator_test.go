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
	apiv1alpha1 "github.com/openmcp-project/service-provider-metrics-operator/api/v1alpha1"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/flux"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/instance"
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
				for i := range objList.Items {
					obj := &objList.Items[i]
					if err := wait.For(openmcpconditions.Match(obj, onboardingConfig, "Ready", corev1.ConditionTrue),
						wait.WithTimeout(10*time.Minute)); err != nil {
						mo := &apiv1alpha1.MetricsOperator{}
						if getErr := onboardingConfig.Client().Resources().Get(ctx, obj.GetName(), obj.GetNamespace(), mo); getErr == nil {
							t.Errorf("%v — MetricsOperator status: conditions=%v resources=%+v", err, mo.Status.Conditions, mo.Status.Resources)
						} else {
							t.Error(err)
						}
					}
				}
				objList.DeepCopyInto(&onboardingList)
				return ctx
			},
		).
		Assess("platform cluster: OCIRepository and HelmRelease are ready",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				platformConfig, err := clusterutils.ConfigByPrefix("platform", corev1.NamespaceDefault)
				if err != nil {
					t.Errorf("failed to get platform cluster config: %v", err)
					return ctx
				}
				tenantNamespace, err := libutils.StableMCPNamespace(testMCP, "default")
				if err != nil {
					t.Errorf("failed to get tenant namespace: %v", err)
					return ctx
				}

				ociRepo := &sourcev1.OCIRepository{}
				ociRepo.SetName(flux.OCIRepositoryName)
				ociRepo.SetNamespace(tenantNamespace)
				if err := wait.For(openmcpconditions.Match(ociRepo, platformConfig, "Ready", corev1.ConditionTrue),
					wait.WithTimeout(5*time.Minute)); err != nil {
					t.Errorf("OCIRepository not ready: %v", err)
				}

				helmRelease := &helmv2.HelmRelease{}
				helmRelease.SetName(flux.HelmReleaseName)
				helmRelease.SetNamespace(tenantNamespace)
				if err := wait.For(openmcpconditions.Match(helmRelease, platformConfig, "Ready", corev1.ConditionTrue),
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
				obj := &apiv1alpha1.MetricsOperator{}
				obj.Name = testMCP
				obj.Namespace = corev1.NamespaceDefault
				workloadNamespace := instance.Namespace(obj)
				dep := &appsv1.DeploymentList{}
				if err := wait.For(conditions.New(workloadConfig.Client().Resources(workloadNamespace)).
					ResourceListN(dep, 1),
					wait.WithTimeout(5*time.Minute)); err != nil {
					t.Errorf("metrics-operator deployment not found in namespace %s: %v", workloadNamespace, err)
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
