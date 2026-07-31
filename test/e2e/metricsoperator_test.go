package e2e

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
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
	"github.com/openmcp-project/openmcp-testing/pkg/resources"
	apiv1alpha1 "github.com/openmcp-project/service-provider-metrics-operator/api/v1alpha1"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/flux"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/instance"
)

var testMCP = ""

func TestServiceProvider(t *testing.T) {
	var onboardingList unstructured.UnstructuredList
	basicProviderTest := features.New("metrics-operator provider test").
		Setup(func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			if _, err := resources.CreateObjectsFromDir(ctx, c, "platform"); err != nil {
				t.Errorf("failed to create platform cluster objects: %v", err)
			}
			return ctx
		}).
		Assess("create MetricsOperator and verify Ready",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				onboardingConfig, err := clusterutils.OnboardingConfig()
				if err != nil {
					t.Error(err)
					return ctx
				}
				var objList *unstructured.UnstructuredList
				if err := wait.For(func(ctx context.Context) (bool, error) {
					var createErr error
					objList, createErr = resources.CreateObjectsFromDir(ctx, onboardingConfig, "onboarding")
					if createErr != nil {
						if strings.Contains(createErr.Error(), "no matches for") {
							return false, nil
						}
						return false, createErr
					}
					return true, nil
				}, wait.WithTimeout(5*time.Minute), wait.WithInterval(5*time.Second)); err != nil {
					t.Errorf("failed to create onboarding cluster objects: %v", err)
					return ctx
				}
				for i := range objList.Items {
					if objList.Items[i].GetKind() == "ControlPlane" {
						testMCP = objList.Items[i].GetName()
					}
				}
				var wg sync.WaitGroup
				for i := range objList.Items {
					obj := &objList.Items[i]
					conditionType := "Ready"
					if obj.GetKind() == "ControlPlane" {
						conditionType = "AllAccessReady"
					}
					wg.Add(1)
					go func(obj *unstructured.Unstructured, conditionType string) {
						defer wg.Done()
						if err := wait.For(openmcpconditions.Match(obj, onboardingConfig, conditionType, corev1.ConditionTrue),
							wait.WithTimeout(5*time.Minute)); err != nil {
							mo := &apiv1alpha1.MetricsOperator{}
							if getErr := onboardingConfig.Client().Resources().Get(ctx, obj.GetName(), obj.GetNamespace(), mo); getErr == nil {
								t.Errorf("%v — MetricsOperator status: conditions=%v resources=%+v", err, mo.Status.Conditions, mo.Status.Resources)
							} else {
								t.Error(err)
							}
						}
					}(obj, conditionType)
				}
				wg.Wait()
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

				g, gctx := errgroup.WithContext(ctx)
				g.Go(func() error {
					ociRepo := &sourcev1.OCIRepository{}
					ociRepo.SetName(flux.OCIRepositoryName)
					ociRepo.SetNamespace(tenantNamespace)
					return wait.For(openmcpconditions.Match(ociRepo, platformConfig, "Ready", corev1.ConditionTrue),
						wait.WithContext(gctx), wait.WithTimeout(5*time.Minute))
				})
				g.Go(func() error {
					helmRelease := &helmv2.HelmRelease{}
					helmRelease.SetName(flux.HelmReleaseName)
					helmRelease.SetNamespace(tenantNamespace)
					return wait.For(openmcpconditions.Match(helmRelease, platformConfig, "Ready", corev1.ConditionTrue),
						wait.WithContext(gctx), wait.WithTimeout(5*time.Minute))
				})
				if err := g.Wait(); err != nil {
					t.Errorf("platform cluster resources not ready: %v", err)
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
				onboardingConfig, err := clusterutils.OnboardingConfig()
				if err != nil {
					t.Error(err)
					return ctx
				}
				if err := apiv1alpha1.AddToScheme(onboardingConfig.Client().Resources().GetScheme()); err != nil {
					t.Errorf("failed to register MetricsOperator scheme: %v", err)
					return ctx
				}
				obj := &apiv1alpha1.MetricsOperator{}
				if err := onboardingConfig.Client().Resources().Get(ctx, testMCP, corev1.NamespaceDefault, obj); err != nil {
					t.Errorf("failed to get MetricsOperator %s/%s: %v", corev1.NamespaceDefault, testMCP, err)
					return ctx
				}
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
		Assess("apply Metric to MCP cluster",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				platformConfig, err := clusterutils.ConfigByPrefix("platform", corev1.NamespaceDefault)
				if err != nil {
					t.Errorf("failed to get platform cluster config: %v", err)
					return ctx
				}
				// The Metric CRD is installed on the MCP cluster by the metrics-operator once
				// it is running, so retry until the CRD and the MCP cluster are available.
				if err := wait.For(func(ctx context.Context) (bool, error) {
					_, createErr := clusterutils.ImportToMCPCluster(ctx, platformConfig, testMCP, "mcp")
					if createErr != nil {
						if strings.Contains(createErr.Error(), "no matches for") ||
							strings.Contains(createErr.Error(), "not found") {
							return false, nil
						}
						return false, createErr
					}
					return true, nil
				}, wait.WithTimeout(5*time.Minute), wait.WithInterval(5*time.Second)); err != nil {
					t.Errorf("failed to create mcp-workload objects on MCP cluster: %v", err)
				}
				return ctx
			},
		).
		Assess("delete MetricsOperator: blocked while Metric CRs exist, unblocked after cleanup",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				onboardingConfig, err := clusterutils.OnboardingConfig()
				if err != nil {
					t.Error(err)
					return ctx
				}
				platformConfig, err := clusterutils.ConfigByPrefix("platform", corev1.NamespaceDefault)
				if err != nil {
					t.Errorf("failed to get platform cluster config: %v", err)
					return ctx
				}

				// Trigger deletion of the MetricsOperator.
				mo := &apiv1alpha1.MetricsOperator{}
				if err := onboardingConfig.Client().Resources().Get(ctx, testMCP, corev1.NamespaceDefault, mo); err != nil {
					t.Errorf("failed to get MetricsOperator: %v", err)
					return ctx
				}
				if err := onboardingConfig.Client().Resources().Delete(ctx, mo); err != nil {
					t.Errorf("failed to delete MetricsOperator: %v", err)
					return ctx
				}

				// MetricsOperator must stay in Terminating while Metric CRs remain on the MCP.
				if err := wait.For(openmcpconditions.Match(mo, onboardingConfig, "Ready", corev1.ConditionFalse),
					wait.WithTimeout(30*time.Second)); err != nil {
					t.Errorf("MetricsOperator did not reach non-Ready within 30s (may already be gone — that's a bug): %v", err)
				}

				// Remove the Metric CRs from the MCP so deletion can proceed.
				metricList, delErr := clusterutils.ImportToMCPCluster(ctx, platformConfig, testMCP, "mcp")
				if delErr != nil {
					t.Logf("could not list mcp objects for cleanup (CRD may already be gone): %v", delErr)
				} else {
					for i := range metricList.Items {
						obj := &metricList.Items[i]
						mcpConfig, mcpErr := clusterutils.MCPConfig(ctx, platformConfig, testMCP)
						if mcpErr != nil {
							t.Errorf("failed to get MCP config: %v", mcpErr)
							return ctx
						}
						_ = mcpConfig.Client().Resources().Delete(ctx, obj)
					}
				}

				// Now the MetricsOperator must fully delete.
				if err := wait.For(conditions.New(onboardingConfig.Client().Resources()).ResourceDeleted(mo),
					wait.WithTimeout(5*time.Minute)); err != nil {
					t.Errorf("MetricsOperator not deleted after Metric CRs were removed: %v", err)
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
				// Don't wait for deletion — the SP finalizer may block if the mcp cluster is gone.
				// The cleanup-e2e-clusters task will handle the rest.
				_ = onboardingConfig.Client().Resources().Delete(ctx, &obj)
			}
			return ctx
		})
	testenv.Test(t, basicProviderTest.Feature())
}
