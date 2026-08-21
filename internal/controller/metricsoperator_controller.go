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
	"slices"
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
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/authn"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/authz"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/configmap"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/cpresources"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/flux"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/helm"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/instance"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/objectutils"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/resources"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/secret"
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
func (r *MetricsOperatorReconciler) CreateOrUpdate(ctx context.Context, obj *apiv1alpha1.MetricsOperator, pc *apiv1alpha1.ProviderConfig, clusters clusteraccess.ClusterContext) (ctrl.Result, error) {
	serviceprovider.StatusProgressing(obj, "Reconciling", "Reconcile in progress")
	mgr, err := r.createObjectManager(ctx, obj, pc, clusters)
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
func (r *MetricsOperatorReconciler) Delete(ctx context.Context, obj *apiv1alpha1.MetricsOperator, pc *apiv1alpha1.ProviderConfig, clusters clusteraccess.ClusterContext) (ctrl.Result, error) {
	blockingKinds, err := cpresources.BlockingKinds(ctx, clusters.MCPCluster.Client())
	if err != nil {
		serviceprovider.StatusProgressing(obj, conditionReasonError, err.Error())
		return ctrl.Result{}, err
	}
	if len(blockingKinds) > 0 {
		msg := fmt.Sprintf("waiting for user resources to be deleted: %s", strings.Join(blockingKinds, ", "))
		serviceprovider.StatusTerminatingWithReason(obj, "UserResourcesExist", msg)
		return ctrl.Result{RequeueAfter: time.Second * 5}, nil
	}
	serviceprovider.StatusTerminating(obj)

	mgr, err := r.createObjectManager(ctx, obj, pc, clusters)
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

func updateStatusError(obj *apiv1alpha1.MetricsOperator, resourceErrors bool, err error) error {
	if resourceErrors {
		err = errors.Join(ErrManagedResources, err)
	}
	serviceprovider.StatusProgressing(obj, conditionReasonError, userErrorMessage(err))
	return ctrlerrors.IgnoreInvalidUserInput(err)
}

// userErrorMessage constructs an end-user facing error message.
func userErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	errorMessages := []string{}
	if errors.Is(err, ErrManagedResources) {
		errorMessages = append(errorMessages, ErrManagedResources.Error())
	}
	if errors.Is(err, secret.ErrSecretCleanup) {
		errorMessages = append(errorMessages, secret.ErrSecretCleanup.Error())
	}
	if errors.Is(err, configmap.ErrConfigMapCleanup) {
		errorMessages = append(errorMessages, configmap.ErrConfigMapCleanup.Error())
	}
	if len(errorMessages) == 0 {
		return err.Error()
	}
	return strings.Join(errorMessages, "; ")
}

//nolint:gocyclo
func (r *MetricsOperatorReconciler) createObjectManager(ctx context.Context, obj *apiv1alpha1.MetricsOperator, pc *apiv1alpha1.ProviderConfig, clusters clusteraccess.ClusterContext) (resources.Manager, error) {
	tenantNamespace, err := libutils.StableMCPNamespace(obj.Name, obj.Namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to determine tenant namespace: %w", err)
	}

	if err := r.ensureInstanceID(ctx, obj); err != nil {
		return nil, err
	}

	// select the requested version from the provider config
	moVersion, err := selectMetricsOperatorVersion(obj.Spec.Version, pc)
	if err != nil {
		return nil, err
	}
	helmValues, err := helm.ExtractHelmValues(moVersion.HelmValues)
	if err != nil {
		return nil, fmt.Errorf("failed to extract helm values: %w", err)
	}

	platformCluster := resources.NewManagedCluster(r.PlatformCluster, r.PlatformCluster.RESTConfig(), tenantNamespace, resources.PlatformCluster)
	workloadCluster := resources.NewManagedCluster(clusters.WorkloadCluster, clusters.WorkloadCluster.RESTConfig(), instance.Namespace(obj), resources.WorkloadCluster)
	cpCluster := resources.NewManagedCluster(clusters.MCPCluster, clusters.MCPCluster.RESTConfig(), flux.DefaultNamespace, resources.ControlPlane)

	metricsNamespace := cpCluster.GetDefaultNamespace()
	if helmValues.NamespaceOverride != "" {
		metricsNamespace = helmValues.NamespaceOverride
	}

	// ### CP RESOURCES ###
	// ServiceAccount on CP + token Secret on workload cluster so --install-crds connects to CP.
	cpServiceAccount := &authn.ManagedServiceAccount{
		NamespacedName: k8stypes.NamespacedName{
			Name:      "metrics-operator-server",
			Namespace: metricsNamespace,
		},
	}

	cpServiceAccount.Configure(workloadCluster, cpCluster, pc.PollInterval())

	moVersion.HelmValues, err = helm.AddAuthToHelmValues(moVersion.HelmValues, cpCluster, cpServiceAccount.KubeAPIAccess())
	if err != nil {
		return nil, fmt.Errorf("failed to inject CP auth into helm values: %w", err)
	}
	moVersion.HelmValues, err = helm.AddDefaultHelmValues(moVersion.HelmValues, metricsNamespace)
	if err != nil {
		return nil, fmt.Errorf("failed to set default helm values: %w", err)
	}
	authz.Configure(cpCluster, cpServiceAccount)

	// Sync image pull secrets from platform cluster to workload
	secret.ManagePullSecrets(workloadCluster, helmValues.Global.ImagePullSecrets, secret.SecretCopyConfig{
		SourceClient:    r.PlatformCluster.Client(),
		SourceNamespace: r.PodNamespace,
		TargetNamespace: instance.Namespace(obj),
	})

	// Sync chart pull secret within platform cluster from pod namespace to tenant namespace
	var prefixedChartPullSecret string
	if moVersion.ChartPullSecret != "" {
		prefixedChartPullSecret, err = secret.PrefixSecretName(moVersion.ChartPullSecret)
		if err != nil {
			return nil, fmt.Errorf("error generating secret name: %w", err)
		}
		secret.ManagePullSecrets(platformCluster, []corev1.LocalObjectReference{
			{Name: moVersion.ChartPullSecret},
		}, secret.SecretCopyConfig{
			SourceClient:    r.PlatformCluster.Client(),
			SourceNamespace: r.PodNamespace,
			TargetNamespace: tenantNamespace,
			TargetName:      prefixedChartPullSecret,
		})
	}

	if pc.Spec.CABundleRef != nil {
		// add custom ca volume, volumeMount and envVar to helm values
		moVersion.HelmValues, err = helm.AddCAHelmValues(moVersion.HelmValues, pc.Spec.CABundleRef)
		if err != nil {
			return nil, fmt.Errorf("failed to add ca volume to helm values: %w", err)
		}

		// Sync ca configmap from platform cluster to MCP
		configmap.ManageCaConfigMap(workloadCluster, pc.Spec.CABundleRef.LocalObjectReference, configmap.ConfigMapCopyConfig{
			SourceClient:    r.PlatformCluster.Client(),
			SourceNamespace: r.PodNamespace,
			TargetNamespace: instance.Namespace(obj),
			TargetName:      helm.CustomCABundleConfigMapName,
		})

	}

	flux.ManageFluxResources(flux.ManageFluxResourcesParams{
		Cluster:             platformCluster,
		CPNamespace:         metricsNamespace,
		WorkloadNamespace:   instance.Namespace(obj),
		ChartPullSecretName: prefixedChartPullSecret,
		Obj:                 obj,
		Interval:            pc.PollInterval(),
		ClusterContext:      clusters,
		RequestedVersion:    moVersion,
	})

	mgr := resources.NewManager()
	mgr.AddCluster(cpCluster)
	mgr.AddCluster(workloadCluster)
	mgr.AddCluster(platformCluster)

	// create cleaner to remove orphaned pull secret copies from platform tenant namespace
	platformCleaner := secret.NewSecretCleaner(platformCluster, tenantNamespace, []corev1.LocalObjectReference{
		{
			Name: prefixedChartPullSecret,
		},
	})
	mgr.AddCleaner(platformCleaner)

	// create cleaner to remove orphaned pull secret copies from workload cluster
	secretsToKeep := append(slices.Clone(helmValues.Global.ImagePullSecrets), corev1.LocalObjectReference{Name: cpServiceAccount.KubeAPIAccess()})
	workloadSecretCleaner := secret.NewSecretCleaner(workloadCluster, instance.Namespace(obj), secretsToKeep)
	mgr.AddCleaner(workloadSecretCleaner)

	// create cleaner to remove orphaned configmaps from workload cluster
	configMapsToKeep := []corev1.LocalObjectReference{}
	if pc.Spec.CABundleRef != nil {
		configMapsToKeep = append(configMapsToKeep, corev1.LocalObjectReference{Name: helm.CustomCABundleConfigMapName})
	}
	controlPlaneConfigMapCleaner := configmap.NewConfigMapCleaner(workloadCluster, tenantNamespace, configMapsToKeep)
	mgr.AddCleaner(controlPlaneConfigMapCleaner)

	return mgr, nil
}

func selectMetricsOperatorVersion(requestedVersion string, pc *apiv1alpha1.ProviderConfig) (apiv1alpha1.MetricsOperatorVersion, error) {
	for _, v := range pc.Spec.Versions {
		if v.Version == requestedVersion {
			return v, nil
		}
	}
	return apiv1alpha1.MetricsOperatorVersion{}, fmt.Errorf("%w: requested version (%s) is not available", ctrlerrors.ErrInvalidUserInput, requestedVersion)
}

func resultsToResources(ctx context.Context, results []resources.Result) ([]apiv1alpha1.ManagedResource, bool) {
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
			l.Error(res.Error, "resource reconcile failed", "object", objectutils.ObjectID(obj))
		}
	}
	return resources, containsError
}

func nilIfEmptyString(str string) *string {
	if str == "" {
		return nil
	}
	return ptr.To(str)
}

func allResourcesReady(resources []apiv1alpha1.ManagedResource) bool {
	for _, res := range resources {
		if res.Phase != apiv1alpha1.Ready {
			return false
		}
	}
	return true
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
