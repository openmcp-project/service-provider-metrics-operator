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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrlerrors "github.com/openmcp-project/controller-utils/pkg/errors"
	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider"
	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider/clusteraccess"
	libutils "github.com/openmcp-project/openmcp-operator/lib/utils"

	apiv1alpha1 "github.com/openmcp-project/service-provider-metrics-operator/api/v1alpha1"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator"
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
	mgr, err := r.createObjectManager(obj, pc, clusterCtx)
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
	mgr, err := r.createObjectManager(obj, pc, clusterCtx)
	if err != nil {
		serviceprovider.StatusProgressing(obj, conditionReasonError, err.Error())
		return ctrl.Result{}, ctrlerrors.IgnoreInvalidUserInput(err)
	}
	results, err := mgr.Delete(ctx)
	managedResources, resultContainsErrors := resultsToResources(ctx, results)
	obj.Status.Resources = managedResources
	if metricsoperator.AllDeleted(results) {
		return ctrl.Result{}, nil
	}
	if resultContainsErrors || err != nil {
		return ctrl.Result{}, updateStatusError(obj, resultContainsErrors, err)
	}
	return ctrl.Result{RequeueAfter: time.Second * 5}, nil
}

// IsReferencedSecret returns true if the given secret should trigger reconciliation.
func (r *MetricsOperatorReconciler) IsReferencedSecret(ctx context.Context, secret *corev1.Secret, pc *apiv1alpha1.ProviderConfig) bool {
	if pc == nil {
		return false
	}
	for _, version := range pc.Spec.Versions {
		if version.ChartPullSecret == secret.Name {
			return true
		}
		helmValues, err := metricsoperator.ExtractHelmValues(version.HelmValues)
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

func (r *MetricsOperatorReconciler) createObjectManager(obj *apiv1alpha1.MetricsOperator, pc *apiv1alpha1.ProviderConfig, clusterCtx clusteraccess.ClusterContext) (metricsoperator.Manager, error) {
	tenantNamespace, err := libutils.StableMCPNamespace(obj.Name, obj.Namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to determine tenant namespace: %w", err)
	}
	version, err := selectVersion(obj.Spec.Version, pc)
	if err != nil {
		return nil, err
	}
	helmValues, err := metricsoperator.ExtractHelmValues(version.HelmValues)
	if err != nil {
		return nil, fmt.Errorf("failed to extract helm values: %w", err)
	}

	platformCluster := metricsoperator.NewManagedCluster(r.PlatformCluster, r.PlatformCluster.RESTConfig(), tenantNamespace, metricsoperator.ClusterTypePlatform)

	metricsNamespace := metricsoperator.DefaultNamespace
	if helmValues.NamespaceOverride != "" {
		metricsNamespace = helmValues.NamespaceOverride
	}

	mcpCluster := metricsoperator.NewManagedCluster(clusterCtx.MCPCluster, clusterCtx.MCPCluster.RESTConfig(), metricsNamespace, metricsoperator.ClusterTypeManagedControlPlane)

	// ServiceAccount on MCP + token Secret on workload cluster so --install-crds connects to MCP.
	mcpSA := &metricsoperator.ManagedServiceAccount{
		NamespacedName: k8stypes.NamespacedName{
			Name:      "metrics-operator-server",
			Namespace: metricsNamespace,
		},
	}

	// Workload cluster: used to sync image pull secrets so Flux can pull the chart image.
	workloadCluster := metricsoperator.NewManagedCluster(clusterCtx.WorkloadCluster, clusterCtx.WorkloadCluster.RESTConfig(), metricsNamespace, metricsoperator.ClusterTypeWorkload)

	mcpSA.Configure(workloadCluster, mcpCluster, pc.PollInterval())
	metricsoperator.ConfigureAuthz(mcpCluster, mcpSA)

	version.HelmValues, err = metricsoperator.AddAuthToHelmValues(version.HelmValues, mcpCluster, mcpSA)
	if err != nil {
		return nil, fmt.Errorf("failed to inject MCP auth into helm values: %w", err)
	}
	version.HelmValues, err = metricsoperator.AddDefaultHelmValues(version.HelmValues, metricsNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to set default helm values: %w", err)
	}

	// Sync image pull secrets to workload cluster (metrics-operator namespace).
	for _, imagePullSecret := range helmValues.ImagePullSecrets {
		metricsoperator.ManagePullSecret(workloadCluster, imagePullSecret, metricsoperator.SecretCopyConfig{
			SourceClient:    platformCluster.GetClient(),
			SourceNamespace: r.PodNamespace,
			TargetNamespace: metricsNamespace,
			TargetName:      imagePullSecret.Name,
		})
	}

	// Sync chart pull secret to platform cluster (tenant namespace).
	var prefixedChartPullSecret string
	if version.ChartPullSecret != "" {
		prefixedChartPullSecret, err = metricsoperator.PrefixSecretName(version.ChartPullSecret)
		if err != nil {
			return nil, fmt.Errorf("error generating secret name: %w", err)
		}
		metricsoperator.ManagePullSecret(platformCluster, corev1.LocalObjectReference{Name: version.ChartPullSecret}, metricsoperator.SecretCopyConfig{
			SourceClient:    platformCluster.GetClient(),
			SourceNamespace: r.PodNamespace,
			TargetNamespace: tenantNamespace,
			TargetName:      prefixedChartPullSecret,
		})
	}

	metricsoperator.ManageFluxResources(metricsoperator.ManageFluxResourcesParams{
		Cluster:             platformCluster,
		WorkloadNamespace:   metricsNamespace,
		ChartPullSecretName: prefixedChartPullSecret,
		Obj:                 obj,
		Interval:            pc.PollInterval(),
		ClusterContext:      clusterCtx,
		RequestedVersion:    version,
	})

	mgr := metricsoperator.NewManager()
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

func resultsToResources(ctx context.Context, results []metricsoperator.Result) ([]apiv1alpha1.ManagedResource, bool) {
	l := log.FromContext(ctx)
	containsError := false
	resources := make([]apiv1alpha1.ManagedResource, 0, len(results))
	for _, res := range results {
		obj := res.Object.GetObject()
		status := res.Object.GetStatus(apiv1alpha1.ResourceLocation(res.Cluster.GetClusterType()))
		resources = append(resources, apiv1alpha1.ManagedResource{
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
			l.Error(res.Error, "objectID", metricsoperator.ObjectID(obj))
		}
	}
	return resources, containsError
}

func nilIfEmptyString(str string) *string {
	if str == "" {
		return nil
	}
	return new(str)
}

func allResourcesReady(resources []apiv1alpha1.ManagedResource) bool {
	for _, res := range resources {
		if res.Phase != apiv1alpha1.Ready {
			return false
		}
	}
	return true
}
