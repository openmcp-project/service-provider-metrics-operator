package e2e

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
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
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/helm"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/instance"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/meta"
	klientresources "sigs.k8s.io/e2e-framework/klient/k8s/resources"
)

const (
	mcpCAConfigMapName    = "custom-ca-bundle"
	caConfigMapNameUpdate = "ca-bundle-update"
	caConfigMapKey        = "ca.crt"
	caVolumeName          = "custom-ca-bundle"
	caMountPath           = "/etc/open-control-plane/custom-ca"
	sslCertDirEnvName     = "SSL_CERT_DIR"
	sslCertDirEnvValue    = "/etc/ssl/certs:/etc/pki/tls/certs:/etc/open-control-plane/custom-ca"
)

var testCP = ""

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
						testCP = objList.Items[i].GetName()
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
		Assess("platform cluster: OCIRepository, HelmRelease and ChartPullSecret are ready",
			func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
				platformConfig, err := clusterutils.ConfigByPrefix("platform", corev1.NamespaceDefault)
				if err != nil {
					t.Errorf("failed to get platform cluster config: %v", err)
					return ctx
				}
				tenantNamespace, err := libutils.StableMCPNamespace(testCP, "default")
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
				g.Go(func() error {
					chartPullSecret := &corev1.Secret{}
					chartPullSecret.SetName("sp-mo-registry-credentials")
					chartPullSecret.SetNamespace(tenantNamespace)
					chartPullSecrets := &corev1.SecretList{
						Items: []corev1.Secret{*chartPullSecret},
					}
					return wait.For(conditions.New(platformConfig.Client().Resources()).ResourcesFound(chartPullSecrets),
						wait.WithContext(gctx), wait.WithTimeout(5*time.Minute))
				})
				if err := g.Wait(); err != nil {
					t.Errorf("platform cluster resources not ready: %v", err)
				}
				return ctx
			},
		).
		Assess("workload cluster: metrics-operator deployment and imagePullSecret exists",
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
				if err := onboardingConfig.Client().Resources().Get(ctx, testCP, corev1.NamespaceDefault, obj); err != nil {
					t.Errorf("failed to get MetricsOperator %s/%s: %v", corev1.NamespaceDefault, testCP, err)
					return ctx
				}
				workloadNamespace := instance.Namespace(obj)
				dep := &appsv1.DeploymentList{}
				if err := wait.For(conditions.New(workloadConfig.Client().Resources(workloadNamespace)).
					ResourceListN(dep, 1),
					wait.WithTimeout(5*time.Minute)); err != nil {
					t.Errorf("metrics-operator deployment not found in namespace %s: %v", workloadNamespace, err)
				}
				imagePullSecret := &corev1.Secret{}
				imagePullSecret.SetName("registry-credentials")
				imagePullSecret.SetNamespace(workloadNamespace)
				list := &corev1.SecretList{
					Items: []corev1.Secret{*imagePullSecret},
				}
				if err := wait.For(conditions.New(workloadConfig.Client().Resources()).ResourcesFound(list), wait.WithTimeout(2*time.Minute)); err != nil {
					t.Errorf("image pull secret not found in namespace %s: %v", workloadNamespace, err)
				}
				caBundleConfigMap := &corev1.ConfigMap{}
				caBundleConfigMap.SetName(mcpCAConfigMapName)
				caBundleConfigMap.SetNamespace(workloadNamespace)
				cmList := &corev1.ConfigMapList{
					Items: []corev1.ConfigMap{*caBundleConfigMap},
				}
				if err := wait.For(conditions.New(workloadConfig.Client().Resources()).ResourcesFound(cmList), wait.WithTimeout(2*time.Minute)); err != nil {
					t.Errorf("ca configmap not found on workload cluster: %v", err)
				}
				return ctx
			},
		).
		Assess("provider config update with new secret references", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			platformConfig, err := clusterutils.ConfigByPrefix("platform", corev1.NamespaceDefault)
			if err != nil {
				t.Errorf("failed to get platform cluster config: %v", err)
				return ctx
			}
			if err := apiv1alpha1.AddToScheme(platformConfig.Client().Resources().GetScheme()); err != nil {
				t.Errorf("failed to add api types to client scheme: %s", err)
				return ctx
			}
			providerConfig := &apiv1alpha1.ProviderConfig{}
			if err := platformConfig.Client().Resources().Get(ctx, "metricsoperator", "openmcp-system", providerConfig); err != nil {
				t.Errorf("failed to get provider config: %v", err)
				return ctx
			}
			providerConfig.Spec.Versions[0].ChartPullSecret = "registry-credentials-update"
			values := helm.HelmValues{
				ImagePullSecrets: []corev1.LocalObjectReference{
					{Name: "registry-credentials-update"},
				},
			}
			bytes, err := json.Marshal(values)
			if err != nil {
				t.Errorf("failed to marshal helm values: %v", err)
				return ctx
			}
			providerConfig.Spec.Versions[0].HelmValues = &v1.JSON{Raw: bytes}
			if err := platformConfig.Client().Resources().Update(ctx, providerConfig); err != nil {
				t.Errorf("failed to update provider config: %v", err)
				return ctx
			}
			// verify service stays healthy
			onboardingConfig, err := clusterutils.OnboardingConfig()
			if err != nil {
				t.Error(err)
				return ctx
			}
			err = apiv1alpha1.AddToScheme(onboardingConfig.GetClient().Resources().GetScheme())
			if err != nil {
				t.Error(err)
				return ctx
			}
			mo := &apiv1alpha1.MetricsOperator{}
			mo.SetName(testCP)
			mo.SetNamespace(corev1.NamespaceDefault)
			if err := wait.For(openmcpconditions.Match(mo, onboardingConfig, "Ready", corev1.ConditionTrue), wait.WithTimeout(2*time.Minute)); err != nil {
				t.Errorf("MetricsOperator not ready after provider config update: %v", err)
			}
			return ctx
		}).
		Assess("platform chart pull secret updated", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			platformConfig, err := clusterutils.ConfigByPrefix("platform", corev1.NamespaceDefault)
			if err != nil {
				t.Errorf("failed to get platform cluster config: %v", err)
				return ctx
			}
			tenantNamespace, err := libutils.StableMCPNamespace(testCP, "default")
			if err != nil {
				t.Errorf("failed to get tenant namespace: %v", err)
				return ctx
			}
			chartSecret := &corev1.Secret{}
			chartSecret.SetName("sp-mo-registry-credentials")
			chartSecret.SetNamespace(tenantNamespace)
			if err := wait.For(conditions.New(platformConfig.Client().Resources()).ResourceDeleted(chartSecret), wait.WithTimeout(2*time.Minute)); err != nil {
				t.Errorf("orphaned chart pull secret is not deleted: %v", err)
			}
			chartSecret.SetName("sp-mo-registry-credentials-update")
			if err := wait.For(conditions.New(platformConfig.Client().Resources()).ResourcesFound(&corev1.SecretList{
				Items: []corev1.Secret{*chartSecret},
			}), wait.WithTimeout(2*time.Minute)); err != nil {
				t.Errorf("pull secret not found: %v", err)
			}
			return ctx
		}).
		Assess("workload image pull secrets updated", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			workloadConfig, err := clusterutils.ConfigByPrefix("workload", corev1.NamespaceDefault)
			if err != nil {
				t.Error(err)
				return ctx
			}
			onboardingConfig, err := clusterutils.OnboardingConfig()
			if err != nil {
				t.Errorf("failed to get onboarding cluster config: %v", err)
				return ctx
			}
			if err := apiv1alpha1.AddToScheme(onboardingConfig.Client().Resources().GetScheme()); err != nil {
				t.Errorf("failed to add scheme: %v", err)
				return ctx
			}
			obj := &apiv1alpha1.MetricsOperator{}
			if err := onboardingConfig.Client().Resources().Get(ctx, testCP, corev1.NamespaceDefault, obj); err != nil {
				t.Errorf("failed to get fetch MetricsOperator object: %v", err)
				return ctx
			}
			imagePullSecret := &corev1.Secret{}
			imagePullSecret.SetName("registry-credentials")
			imagePullSecret.SetNamespace(instance.Namespace(obj))
			if err := wait.For(conditions.New(workloadConfig.Client().Resources()).ResourceDeleted(imagePullSecret), wait.WithTimeout(2*time.Minute)); err != nil {
				t.Errorf("orphaned image pull secret is not deleted: %v", err)
				return ctx
			}

			imagePullSecret.SetName("registry-credentials-update")
			list := &corev1.SecretList{
				Items: []corev1.Secret{*imagePullSecret},
			}
			if err := wait.For(conditions.New(workloadConfig.Client().Resources()).ResourcesFound(list), wait.WithTimeout(2*time.Minute)); err != nil {
				t.Errorf("image pull secret not found on workload: %v", err)
				return ctx
			}
			return ctx
		}).
		Assess("provider config update with new ca bundle reference", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			platformConfig, err := clusterutils.ConfigByPrefix("platform", corev1.NamespaceDefault)
			if err != nil {
				t.Errorf("failed to get platform cluster config: %v", err)
				return ctx
			}
			if err := apiv1alpha1.AddToScheme(platformConfig.Client().Resources().GetScheme()); err != nil {
				t.Errorf("failed to add api types to client scheme: %s", err)
				return ctx
			}
			providerConfig := &apiv1alpha1.ProviderConfig{}
			if err := platformConfig.Client().Resources().Get(ctx, "metricsoperator", "openmcp-system", providerConfig); err != nil {
				t.Errorf("failed to get provider config: %v", err)
				return ctx
			}
			providerConfig.Spec.CABundleRef = &corev1.ConfigMapKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: caConfigMapNameUpdate},
				Key:                  caConfigMapKey,
			}
			if err := platformConfig.Client().Resources().Update(ctx, providerConfig); err != nil {
				t.Errorf("failed to update provider config: %v", err)
				return ctx
			}

			onboardingConfig, err := clusterutils.OnboardingConfig()
			apiv1alpha1.AddToScheme(onboardingConfig.GetClient().Resources().GetScheme())
			if err != nil {
				t.Error(err)
				return ctx
			}

			mo := &apiv1alpha1.MetricsOperator{}
			mo.SetName(testCP)
			mo.SetNamespace(corev1.NamespaceDefault)
			if err := wait.For(openmcpconditions.Match(mo, onboardingConfig, "Ready", corev1.ConditionTrue), wait.WithTimeout(2*time.Minute)); err != nil {
				t.Errorf("MetricsOperator not ready after provider config update: %v", err)
			}
			return ctx
		}).
		Assess("workload ca configmap updated", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			workloadConfig, err := clusterutils.ConfigByPrefix("workload", corev1.NamespaceDefault)
			if err != nil {
				t.Error(err)
				return ctx
			}
			onboardingConfig, err := clusterutils.OnboardingConfig()
			if err != nil {
				t.Errorf("failed to get onboarding cluster config: %v", err)
				return ctx
			}
			if err := apiv1alpha1.AddToScheme(onboardingConfig.Client().Resources().GetScheme()); err != nil {
				t.Errorf("failed to add scheme: %v", err)
				return ctx
			}
			obj := &apiv1alpha1.MetricsOperator{}
			if err := onboardingConfig.Client().Resources().Get(ctx, testCP, corev1.NamespaceDefault, obj); err != nil {
				t.Errorf("failed to get fetch MetricsOperator object: %v", err)
				return ctx
			}
			workloadNamespace := instance.Namespace(obj)

			// Verify that updated configmap exists
			mcpCaConfigMap := &corev1.ConfigMap{}
			mcpCaConfigMap.SetName(mcpCAConfigMapName)
			mcpCaConfigMap.SetNamespace(workloadNamespace)
			list := &corev1.ConfigMapList{
				Items: []corev1.ConfigMap{*mcpCaConfigMap},
			}

			if err := wait.For(conditions.New(workloadConfig.Client().Resources()).ResourcesFound(list), wait.WithTimeout(2*time.Minute)); err != nil {
				t.Errorf("ca configmap not found on workload cluster: %v", err)
				return ctx
			}

			// Verify the configmap contains updated certificate data
			if err := workloadConfig.Client().Resources().Get(ctx, mcpCAConfigMapName, workloadNamespace, mcpCaConfigMap); err != nil {
				t.Errorf("failed to get ca configmap data: %v", err)
				return ctx
			}
			caData, ok := mcpCaConfigMap.Data[caConfigMapKey]
			if !ok {
				t.Errorf("ca configmap missing key %s", caConfigMapKey)
				return ctx
			}
			// Verify the data contains the expected updated certificate marker
			if !strings.Contains(caData, "UpdatedDummyCertificate") {
				t.Errorf("ca configmap does not contain expected updated certificate data. Got: %s", caData)
			}
			return ctx
		}).
		Assess("provider config update drops pull secrets", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			platformConfig, err := clusterutils.ConfigByPrefix("platform", corev1.NamespaceDefault)
			if err != nil {
				t.Errorf("failed to get platform cluster config: %v", err)
				return ctx
			}
			if err := apiv1alpha1.AddToScheme(platformConfig.Client().Resources().GetScheme()); err != nil {
				t.Errorf("failed to add api types to client scheme: %s", err)
				return ctx
			}
			providerConfig := &apiv1alpha1.ProviderConfig{}
			if err := platformConfig.Client().Resources().Get(ctx, "metricsoperator", "openmcp-system", providerConfig); err != nil {
				t.Errorf("failed to get provider config: %v", err)
				return ctx
			}
			providerConfig.Spec.Versions[0].ChartPullSecret = ""
			providerConfig.Spec.Versions[0].HelmValues = nil
			providerConfig.Spec.CABundleRef = nil
			if err := platformConfig.Client().Resources().Update(ctx, providerConfig); err != nil {
				t.Errorf("failed to update provider config: %v", err)
			}
			// verify service stays healthy
			onboardingConfig, err := clusterutils.OnboardingConfig()
			if err != nil {
				t.Error(err)
				return ctx
			}
			apiv1alpha1.AddToScheme(onboardingConfig.GetClient().Resources().GetScheme())
			mo := &apiv1alpha1.MetricsOperator{}
			mo.SetName(testCP)
			mo.SetNamespace(corev1.NamespaceDefault)
			if err := wait.For(openmcpconditions.Match(mo, onboardingConfig, "Ready", corev1.ConditionTrue), wait.WithTimeout(2*time.Minute)); err != nil {
				t.Errorf("MetricsOperator not ready after provider config update: %v", err)
			}
			return ctx
		}).
		Assess("platform chart pull secret deleted", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
			platformConfig, err := clusterutils.ConfigByPrefix("platform", corev1.NamespaceDefault)
			if err != nil {
				t.Errorf("failed to get platform cluster config: %v", err)
				return ctx
			}
			tenantNamespace, err := libutils.StableMCPNamespace(testCP, "default")
			if err != nil {
				t.Errorf("failed to get tenant namespace: %v", err)
				return ctx
			}
			spMoSecrets := &corev1.SecretList{}
			if err := wait.For(conditions.New(platformConfig.Client().Resources().WithNamespace(tenantNamespace)).
				ResourceListN(spMoSecrets, 0, klientresources.WithLabelSelector(
					labels.FormatLabels(map[string]string{meta.LabelManagedBy: meta.LabelManagedByValue}))),
				wait.WithTimeout(2*time.Minute)); err != nil {
				t.Errorf("orphaned chart pull secret is not deleted: %v", err)
			}
			return ctx
		}).
		Assess("workload image pull secrets deleted", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
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
				t.Errorf("failed to add scheme: %v", err)
				return ctx
			}
			obj := &apiv1alpha1.MetricsOperator{}
			if err := onboardingConfig.Client().Resources().Get(ctx, testCP, corev1.NamespaceDefault, obj); err != nil {
				t.Errorf("failed to get MetricsOperator: %v", err)
				return ctx
			}
			secrets := &corev1.SecretList{}
			if err := wait.For(conditions.New(workloadConfig.Client().Resources().WithNamespace(instance.Namespace(obj))).
				ResourceListN(secrets, 0, klientresources.WithLabelSelector(
					labels.FormatLabels(map[string]string{meta.LabelManagedBy: meta.LabelManagedByValue}))),
				wait.WithTimeout(2*time.Minute)); err != nil {
				t.Errorf("orphaned image pull secret is not deleted: %v", err)
			}
			return ctx
		}).
		Assess("control plane ca configmap deleted", func(ctx context.Context, t *testing.T, c *envconf.Config) context.Context {
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
				t.Errorf("failed to add scheme: %v", err)
				return ctx
			}
			obj := &apiv1alpha1.MetricsOperator{}
			if err := onboardingConfig.Client().Resources().Get(ctx, testCP, corev1.NamespaceDefault, obj); err != nil {
				t.Errorf("failed to get MetricsOperator: %v", err)
				return ctx
			}
			caConfigMap := &corev1.ConfigMapList{}
			if err := wait.For(conditions.New(workloadConfig.Client().Resources().WithNamespace(instance.Namespace(obj))).
				ResourceListN(caConfigMap, 0, klientresources.WithLabelSelector(
					labels.FormatLabels(map[string]string{meta.LabelManagedBy: meta.LabelManagedByValue}))),
				wait.WithTimeout(2*time.Minute)); err != nil {
				t.Errorf("orphaned ca configmap is not deleted: %v", err)
			}
			return ctx
		}).
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
					_, createErr := clusterutils.ImportToMCPCluster(ctx, platformConfig, testCP, "mcp")
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
				if err := apiv1alpha1.AddToScheme(onboardingConfig.Client().Resources().GetScheme()); err != nil {
					t.Errorf("failed to register MetricsOperator scheme: %v", err)
					return ctx
				}
				mo := &apiv1alpha1.MetricsOperator{}
				if err := onboardingConfig.Client().Resources().Get(ctx, testCP, corev1.NamespaceDefault, mo); err != nil {
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
				metricList, delErr := clusterutils.ImportToMCPCluster(ctx, platformConfig, testCP, "mcp")
				if delErr != nil {
					t.Logf("could not list mcp objects for cleanup (CRD may already be gone): %v", delErr)
				} else {
					for i := range metricList.Items {
						obj := &metricList.Items[i]
						mcpConfig, mcpErr := clusterutils.MCPConfig(ctx, platformConfig, testCP)
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
