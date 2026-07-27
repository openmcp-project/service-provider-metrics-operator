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

	libutils "github.com/openmcp-project/openmcp-operator/lib/utils"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	ctrlerrors "github.com/openmcp-project/controller-utils/pkg/errors"
	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider"
	clusteraccess "github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider/clusteraccess"

	apiv1alpha1 "github.com/openmcp-project/service-provider-metrics-operator/api/v1alpha1"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/authn"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/authz"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/helm"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/instance"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/objectutils"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/resources"
)

const conditionReasonError = "ReconcileError"

// ErrManagedResources is an end-user facing error if errors are present inside ExternalSecretsOperator.Status.ManagedResources
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
	return ctrl.Result{
		RequeueAfter: time.Second * 5,
	}, nil
}

func updateStatusError(obj *apiv1alpha1.MetricsOperator, resourceErrors bool, err error) error {
	if resourceErrors {
		err = errors.Join(ErrManagedResources, err)
	}
	serviceprovider.StatusProgressing(obj, conditionReasonError, userErrorMessage(err))
	return ctrlerrors.IgnoreInvalidUserInput(err)
}

// userErrorMessage constructs an end-user facing error message.
// Only end-user errors are processed.
func userErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	errorMessages := []string{}
	if errors.Is(err, ErrManagedResources) {
		errorMessages = append(errorMessages, ErrManagedResources.Error())
	}
	if errors.Is(err, metricsoperator.ErrOrphanCleanup) {
		errorMessages = append(errorMessages, metricsoperator.ErrOrphanCleanup.Error())
	}
	return strings.Join(errorMessages, "; ")
}

func (r *MetricsOperatorReconciler) createObjectManager(ctx context.Context, obj *apiv1alpha1.MetricsOperator, pc *apiv1alpha1.ProviderConfig, clusters clusteraccess.ClusterContext) (resources.Manager, error) {
	tenantNamespace, err := libutils.StableMCPNamespace(obj.Name, obj.Namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to determine tenant namespace for external secrets deployment: %w", err)
	}
	err = r.ensureInstanceID(ctx, obj)
	if err != nil {
		return nil, err
	}
	// select the requested version from the provider config
	moVersion, err := selectMetricsOperatorVersion(obj.Spec.Version, pc)
	if err != nil {
		return nil, err
	}

	platformCluster := resources.NewManagedCluster(r.PlatformCluster, r.PlatformCluster.RESTConfig(), tenantNamespace, resources.PlatformCluster)
	workloadCluster := resources.NewManagedCluster(clusters.WorkloadCluster, clusters.WorkloadCluster.RESTConfig(), instance.Namespace(obj), resources.WorkloadCluster)
	mcpCluster := resources.NewManagedCluster(clusters.MCPCluster, clusters.MCPCluster.RESTConfig(), metricsoperator.DefaultNamespace, resources.ManagedControlPlane)

	// ### MCP RESOURCES ###
	// set namespace deletion policy orphan to prevent deleting end user data that we are not aware of
	mcpServiceAccount := authn.ManagedServiceAccount{
		NamespacedName: types.NamespacedName{
			Name:      "metrics-operator-server",
			Namespace: mcpCluster.GetDefaultNamespace(),
		},
	}

	mcpServiceAccount.Configure(workloadCluster, mcpCluster, moVersion.HelmValues, pc.PollInterval())
	moVersion.HelmValues, err = helm.AddAuthToHelmValues(moVersion.HelmValues, mcpCluster, mcpServiceAccount.KubeAPIAccess())
	if err != nil {
		return nil, fmt.Errorf("failed to add auth to helm values: %w", err)
	}
	moVersion.HelmValues, err = helm.AddDefaultHelmValues(moVersion.HelmValues, mcpCluster.GetDefaultNamespace())
	if err != nil {
		return nil, fmt.Errorf("failed to add auth to helm values: %w", err)
	}

	authz.Configure(mcpCluster, &mcpServiceAccount)

	metricsoperator.ManageFluxResources(metricsoperator.ManageFluxResourcesParams{
		Cluster:           platformCluster,
		MCPNamespace:      metricsoperator.DefaultNamespace,
		WorkloadNamespace: instance.Namespace(obj),
		Obj:               obj,
		Interval:          pc.PollInterval(),
		ClusterContext:    clusters,
		RequestedVersion:  moVersion,
	})
	mgr := resources.NewManager()
	mgr.AddCluster(mcpCluster)
	mgr.AddCluster(workloadCluster)
	mgr.AddCluster(platformCluster)

	return mgr, nil
}

func selectMetricsOperatorVersion(requestedVersion string, pc *apiv1alpha1.ProviderConfig) (apiv1alpha1.MetricsOperatorVersion, error) {
	for _, configVersion := range pc.Spec.Versions {
		if configVersion.Version == requestedVersion {
			return configVersion, nil
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
			l.Error(res.Error, "objectID", objectutils.ObjectID(obj))
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

// IsReferencedSecret returns true if the given secret should trigger
// reconciliation. See serviceprovider.SecretWatcher for details.
//
//revive:disable:unused-parameter
func (r *MetricsOperatorReconciler) IsReferencedSecret(ctx context.Context, secret *corev1.Secret, pc *apiv1alpha1.ProviderConfig) bool {
	if pc == nil {
		return false
	}
	// TODO: Check if the secret is referenced in the provider config, for example:
	// for _, ref := range pc.Spec.ImagePullSecrets {
	//     if ref.Name == secret.Name {
	//         return true
	//     }
	// }
	return false
}

// sets an instance id that is used to label every managed resource and create an instance namespace on the workload cluster
func (r *MetricsOperatorReconciler) ensureInstanceID(ctx context.Context, obj *apiv1alpha1.MetricsOperator) error {
	if len(instance.GetID(obj)) == 0 {
		instance.SetID(obj, instance.GenerateID(obj))
		if err := r.OnboardingCluster.Client().Update(ctx, obj); err != nil {
			return fmt.Errorf("failed to set instance id of metrics operator resource %s/%s: %w", obj.Namespace, obj.Name, err)
		}
	}
	return nil
}
