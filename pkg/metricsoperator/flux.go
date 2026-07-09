package metricsoperator

import (
	"context"
	"fmt"
	"time"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	"github.com/fluxcd/pkg/apis/meta"
	"github.com/fluxcd/pkg/runtime/conditions"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider/clusteraccess"

	apiv1alpha1 "github.com/openmcp-project/service-provider-metrics-operator/api/v1alpha1"
)

const (
	// DefaultNamespace is the default namespace where metrics-operator is deployed on the workload cluster.
	DefaultNamespace = "metrics-operator"
	// OCIRepositoryName is the name of the OCIRepository resource.
	OCIRepositoryName = "metrics-operator"
	// HelmReleaseName is the name of the HelmRelease resource.
	HelmReleaseName = "metrics-operator"
)

// ManageFluxResourcesParams groups all parameters to create the required Flux resources.
type ManageFluxResourcesParams struct {
	// Cluster defines where the OCIRepository and HelmRelease are created (platform cluster, tenant namespace).
	Cluster ManagedCluster
	// WorkloadNamespace is the namespace on the workload cluster where metrics-operator is deployed.
	WorkloadNamespace string
	// ChartPullSecretName is the name of the secret copy placed in the tenant namespace.
	ChartPullSecretName string
	// Obj is the tenant API object being reconciled.
	Obj *apiv1alpha1.MetricsOperator
	// Interval defines OCIRepository and HelmRelease reconcile intervals.
	Interval time.Duration
	// ClusterContext of the current reconciliation context.
	ClusterContext clusteraccess.ClusterContext
	// RequestedVersion is the version entry selected from ProviderConfig.
	RequestedVersion apiv1alpha1.MetricsOperatorVersion
}

// ManageFluxResources configures OCIRepository and HelmRelease on the platform cluster.
// The HelmRelease targets the workload cluster via spec.kubeConfig (key difference from ESO).
func ManageFluxResources(p ManageFluxResourcesParams) {
	ociRepo := NewManagedObject(&sourcev1.OCIRepository{
		ObjectMeta: metav1.ObjectMeta{
			Name:      OCIRepositoryName,
			Namespace: p.Cluster.GetDefaultNamespace(),
		},
	}, ManagedObjectContext{
		ReconcileFunc: func(_ context.Context, o client.Object) error {
			repo, ok := o.(*sourcev1.OCIRepository)
			if !ok {
				return fmt.Errorf("expected *sourcev1.OCIRepository, got %T", o)
			}
			if p.RequestedVersion.ChartURL == nil {
				return fmt.Errorf("missing ChartURL for version %s", p.RequestedVersion.Version)
			}
			repo.Spec = sourcev1.OCIRepositorySpec{
				Interval: metav1.Duration{Duration: p.Interval},
				URL:      *p.RequestedVersion.ChartURL,
				Reference: &sourcev1.OCIRepositoryRef{
					Tag: p.RequestedVersion.ChartVersion,
				},
				// No LayerSelector: metrics-operator OCI artifacts have deterministic layer ordering.
			}
			if p.ChartPullSecretName != "" {
				repo.Spec.SecretRef = &meta.LocalObjectReference{
					Name: p.ChartPullSecretName,
				}
			}
			return nil
		},
		DependsOn:      []ManagedObject{},
		DeletionPolicy: Delete,
		StatusFunc:     FluxStatus,
	})
	p.Cluster.AddObject(ociRepo)

	helmRelease := NewManagedObject(&helmv2.HelmRelease{
		ObjectMeta: metav1.ObjectMeta{
			Name:      HelmReleaseName,
			Namespace: p.Cluster.GetDefaultNamespace(),
		},
	}, ManagedObjectContext{
		ReconcileFunc: func(_ context.Context, o client.Object) error {
			release, ok := o.(*helmv2.HelmRelease)
			if !ok {
				return fmt.Errorf("expected *helmv2.HelmRelease, got %T", o)
			}
			release.Spec = helmv2.HelmReleaseSpec{
				Interval: metav1.Duration{Duration: p.Interval},
				ChartRef: &helmv2.CrossNamespaceSourceReference{
					Kind:      "OCIRepository",
					Name:      OCIRepositoryName,
					Namespace: p.Cluster.GetDefaultNamespace(),
				},
				// Targets the workload cluster — key difference from ESO which uses MCPAccessSecretKey.
				KubeConfig: &meta.KubeConfigReference{
					SecretRef: &meta.SecretKeyReference{
						Name: p.ClusterContext.WorkloadAccessSecretKey.Name,
						Key:  "kubeconfig",
					},
				},
				Install: &helmv2.Install{
					Remediation: &helmv2.InstallRemediation{
						Retries: 3,
					},
					CreateNamespace: true,
				},
				DriftDetection: &helmv2.DriftDetection{
					Mode: helmv2.DriftDetectionEnabled,
				},
				Values:           p.RequestedVersion.HelmValues,
				TargetNamespace:  p.WorkloadNamespace,
				StorageNamespace: p.WorkloadNamespace,
			}
			return nil
		},
		DependsOn:      []ManagedObject{ociRepo},
		DeletionPolicy: Delete,
		StatusFunc:     FluxStatus,
	})
	p.Cluster.AddObject(helmRelease)
}

// FluxStatus indicates whether the given Flux object is terminating, pending, or ready.
func FluxStatus(o client.Object, rl apiv1alpha1.ResourceLocation) Status {
	fluxObject := o.(conditions.Getter)
	if !o.GetDeletionTimestamp().IsZero() {
		return Status{Phase: apiv1alpha1.Terminating, Message: "Resource is terminating.", Location: rl}
	}
	if conditions.IsTrue(fluxObject, meta.ReadyCondition) {
		return Status{Phase: apiv1alpha1.Ready, Message: "Resource is ready", Location: rl}
	}
	return Status{Phase: apiv1alpha1.Pending, Message: "Resource is not ready", Location: rl}
}
