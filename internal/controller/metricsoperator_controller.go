/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrlerrors "github.com/openmcp-project/controller-utils/pkg/errors"
	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider"
	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider/clusteraccess"
	libutils "github.com/openmcp-project/openmcp-operator/lib/utils"

	apiv1alpha1 "github.com/openmcp-project/service-provider-metrics-operator/api/v1alpha1"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/authn"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/authz"
	helmutil "github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/helm"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/instance"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/objectutils"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/resources"
)

const conditionReasonError = "ReconcileError"

// ErrManagedResources is an end-user facing error if errors are present in Status.Resources.
var ErrManagedResources = errors.New("resources contain reconcile errors")

// MetricsOperatorReconciler reconciles a MetricsOperator object
type MetricsOperatorReconciler struct {
	// OnboardingCluster is the cluster where this controller watches MetricsOperator resources and reacts to their changes.
	OnboardingCluster *clusters.Cluster
	// PlatformCluster is the cluster where this controller is deployed and configured.
	PlatformCluster *clusters.Cluster
	// PodNamespace is the namespace where this controller is deployed in.
	PodNamespace string
}

// CreateOrUpdate is called on every add or update event
func (r *MetricsOperatorReconciler) CreateOrUpdate(ctx context.Context, obj *apiv1alpha1.MetricsOperator, pc *apiv1alpha1.ProviderConfig, clusterCtx clusteraccess.ClusterContext) (ctrl.Result, error) {
	serviceprovider.StatusProgressing(obj, "Reconciling", "Reconcile in progress")
	mgr, err := r.createObjectManager(ctx, obj, pc, clusterCtx)
	if err != nil {
		serviceprovider.StatusProgressing(obj, conditionReasonError, err.Error())
		return ctrl.Result{}, ctrlerrors.IgnoreInvalidUserInput(err)
	}
	results, err := mgr.Apply(ctx)
	managedResources, resultContainsErrors := resultsToResources(ctx, results)
	obj.Status.Resources = managedResources
	if allResourcesReady(managedResources) {
		serviceprovider.StatusReady(obj)
	}
	if resultContainsErrors || err != nil {
		return ctrl.Result{}, updateStatusError(obj, resultContainsErrors, err)
	}
	return ctrl.Result{}, nil
}

// Delete is called on every delete event
func (r *MetricsOperatorReconciler) Delete(ctx context.Context, obj *apiv1alpha1.MetricsOperator, pc *apiv1alpha1.ProviderConfig, clusterCtx clusteraccess.ClusterContext) (ctrl.Result, error) {
	serviceprovider.StatusTerminating(obj)
	mgr, err := r.createObjectManager(ctx, obj, pc, clusterCtx)
	if err != nil {
		serviceprovider.StatusProgressing(obj, conditionReasonError, err.Error())
		return ctrl.Result{}, ctrlerrors.IgnoreInvalidUserInput(err)
	}
	results, err := mgr.Delete(ctx)
	managedResources, resultContainsErrors := resultsToResources(ctx, results)
	obj.Status.Resources = managedResources
	if resources.AllDeleted(results) {
		return ctrl.Result{}, nil
	}
	if resultContainsErrors || err != nil {
		return ctrl.Result{}, updateStatusError(obj, resultContainsErrors, err)
	}
	return ctrl.Result{RequeueAfter: time.Second * 5}, nil
}

// IsReferencedSecret returns true if the given secret should trigger reconciliation.
func (r *MetricsOperatorReconciler) IsReferencedSecret(_ context.Context, secret *corev1.Secret, pc *apiv1alpha1.ProviderConfig) bool {
	if pc == nil {
		return false
	}
	for _, version := range pc.Spec.Versions {
		if version.ChartPullSecret == secret.Name {
			return true
		}
		helmValues, err := helmutil.ExtractHelmValues(version.HelmValues)
		if err != nil {
			continue
		}
		for _, ref := range helmValues.ImagePullSecrets {
			if ref.Name == secret.Name {
				return true
			}
		}
	}
	return false
}

func (r *MetricsOperatorReconciler) createObjectManager(ctx context.Context, obj *apiv1alpha1.MetricsOperator, pc *apiv1alpha1.ProviderConfig, clusterCtx clusteraccess.ClusterContext) (resources.Manager, error) {
	tenantNamespace, err := libutils.StableMCPNamespace(obj.Name, obj.Namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to determine tenant namespace: %w", err)
	}

	if err := r.ensureInstanceID(ctx, obj); err != nil {
		return nil, err
	}

	version, err := selectVersion(obj.Spec.Version, pc)
	if err != nil {
		return nil, err
	}
	helmValues, err := helmutil.ExtractHelmValues(version.HelmValues)
	if err != nil {
		return nil, fmt.Errorf("failed to extract helm values: %w", err)
	}

	platformCluster := resources.NewManagedCluster(r.PlatformCluster, r.PlatformCluster.RESTConfig(), tenantNamespace, resources.PlatformCluster)

	metricsNamespace := metricsoperator.DefaultNamespace
	if helmValues.NamespaceOverride != "" {
		metricsNamespace = helmValues.NamespaceOverride
	}

	mcpCluster := resources.NewManagedCluster(clusterCtx.MCPCluster, clusterCtx.MCPCluster.RESTConfig(), metricsNamespace, resources.ManagedControlPlane)

	// ServiceAccount on MCP + token Secret on workload cluster so --install-crds connects to MCP.
	mcpSA := &authn.ManagedServiceAccount{
		NamespacedName: k8stypes.NamespacedName{
			Name:      "metrics-operator-server",
			Namespace: metricsNamespace,
		},
	}

	// Workload cluster: used to sync image pull secrets so Flux can pull the chart image.
	workloadCluster := resources.NewManagedCluster(clusterCtx.WorkloadCluster, clusterCtx.WorkloadCluster.RESTConfig(), instance.Namespace(obj), resources.WorkloadCluster)

	mcpSA.Configure(workloadCluster, mcpCluster, version.HelmValues, pc.PollInterval())
	authz.Configure(mcpCluster, mcpSA)

	version.HelmValues, err = helmutil.AddAuthToHelmValues(version.HelmValues, mcpCluster, mcpSA.KubeAPIAccess())
	if err != nil {
		return nil, fmt.Errorf("failed to inject MCP auth into helm values: %w", err)
	}
	version.HelmValues, err = helmutil.AddDefaultHelmValues(version.HelmValues, metricsNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to set default helm values: %w", err)
	}

	prefixedChartPullSecret, err := r.managePullSecrets(platformCluster, workloadCluster, helmValues, version, tenantNamespace, metricsNamespace)
	if err != nil {
		return nil, err
	}

	metricsoperator.ManageFluxResources(metricsoperator.ManageFluxResourcesParams{
		Cluster:             platformCluster,
		MCPNamespace:        metricsNamespace,
		WorkloadNamespace:   instance.Namespace(obj),
		ChartPullSecretName: prefixedChartPullSecret,
		Obj:                 obj,
		Interval:            pc.PollInterval(),
		ClusterContext:      clusterCtx,
		RequestedVersion:    version,
	})

	mgr := resources.NewManager()
	mgr.AddCluster(mcpCluster)
	mgr.AddCluster(workloadCluster)
	mgr.AddCluster(platformCluster)

	platformSecretCleaner := metricsoperator.NewSecretCleaner(platformCluster, tenantNamespace, []corev1.LocalObjectReference{
		{Name: prefixedChartPullSecret},
	})
	workloadSecretCleaner := metricsoperator.NewSecretCleaner(workloadCluster, metricsNamespace, helmValues.ImagePullSecrets)

	mgr.AddCleaner(platformSecretCleaner)
	mgr.AddCleaner(workloadSecretCleaner)

	return mgr, nil
}

func (r *MetricsOperatorReconciler) managePullSecrets(
	platformCluster resources.ManagedCluster,
	workloadCluster resources.ManagedCluster,
	helmValues *helmutil.HelmValues,
	version apiv1alpha1.MetricsOperatorVersion,
	tenantNamespace, metricsNamespace string,
) (string, error) {
	metricsoperator.ManagePullSecrets(workloadCluster, helmValues.ImagePullSecrets, metricsoperator.SecretCopyConfig{
		SourceClient:    platformCluster.GetClient(),
		SourceNamespace: r.PodNamespace,
		TargetNamespace: metricsNamespace,
	})
	if version.ChartPullSecret == "" {
		return "", nil
	}
	prefixed, err := metricsoperator.PrefixSecretName(version.ChartPullSecret)
	if err != nil {
		return "", fmt.Errorf("error generating secret name: %w", err)
	}
	metricsoperator.ManagePullSecret(platformCluster, corev1.LocalObjectReference{Name: version.ChartPullSecret}, metricsoperator.SecretCopyConfig{
		SourceClient:    platformCluster.GetClient(),
		SourceNamespace: r.PodNamespace,
		TargetNamespace: tenantNamespace,
		TargetName:      prefixed,
	})
	return prefixed, nil
}

func (r *MetricsOperatorReconciler) ensureInstanceID(ctx context.Context, obj *apiv1alpha1.MetricsOperator) error {
	if len(instance.GetID(obj)) == 0 {
		instance.SetID(obj, instance.GenerateID(obj))
		if err := r.OnboardingCluster.Client().Update(ctx, obj); err != nil {
			return fmt.Errorf("failed to set instance id of metrics operator resource %s/%s: %w", obj.Namespace, obj.Name, err)
		}
	}
	return nil
}

func selectVersion(requestedVersion string, pc *apiv1alpha1.ProviderConfig) (apiv1alpha1.MetricsOperatorVersion, error) {
	for _, v := range pc.Spec.Versions {
		if v.Version == requestedVersion {
			return v, nil
		}
	}
	return apiv1alpha1.MetricsOperatorVersion{}, fmt.Errorf("%w: requested version (%s) is not available", ctrlerrors.ErrInvalidUserInput, requestedVersion)
}

func updateStatusError(obj *apiv1alpha1.MetricsOperator, resourceErrors bool, err error) error {
	if resourceErrors {
		err = errors.Join(ErrManagedResources, err)
	}
	serviceprovider.StatusProgressing(obj, conditionReasonError, userErrorMessage(err))
	return ctrlerrors.IgnoreInvalidUserInput(err)
}

func userErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	msgs := []string{}
	if errors.Is(err, ErrManagedResources) {
		msgs = append(msgs, ErrManagedResources.Error())
	}
	if errors.Is(err, metricsoperator.ErrOrphanCleanup) {
		msgs = append(msgs, metricsoperator.ErrOrphanCleanup.Error())
	}
	return strings.Join(msgs, "; ")
}

func resultsToResources(ctx context.Context, results []resources.Result) ([]apiv1alpha1.ManagedResource, bool) {
	l := log.FromContext(ctx)
	containsError := false
	managed := make([]apiv1alpha1.ManagedResource, 0, len(results))
	for _, res := range results {
		obj := res.Object.GetObject()
		status := res.Object.GetStatus(apiv1alpha1.ResourceLocation(res.Cluster.GetClusterType()))
		managed = append(managed, apiv1alpha1.ManagedResource{
			TypedObjectReference: corev1.TypedObjectReference{
				Kind:      reflect.TypeOf(obj).Elem().Name(),
				Name:      obj.GetName(),
				Namespace: nilIfEmptyString(obj.GetNamespace()),
			},
			Phase:    status.Phase,
			Message:  status.Message,
			Location: status.Location,
		})
		if res.Error != nil {
			containsError = true
			l.Error(res.Error, "objectID", objectutils.ObjectID(obj))
		}
	}
	return managed, containsError
}

func nilIfEmptyString(str string) *string {
	if str == "" {
		return nil
	}
	return ptr.To(str)
}

func allResourcesReady(res []apiv1alpha1.ManagedResource) bool {
	for _, r := range res {
		if r.Phase != apiv1alpha1.Ready {
			return false
		}
	}
	return true
}
