package configmap

import (
	"context"
	"errors"
	"fmt"
	"slices"

	openmcpresources "github.com/openmcp-project/controller-utils/pkg/resources"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/openmcp-project/service-provider-metrics-operator/api/v1alpha1"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/meta"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/resources"
)

// ErrConfigMapCleanup is an user-facing error that indicates configmap cleanup failures
var ErrConfigMapCleanup = errors.New("configmap cleanup failed")

// ConfigMapCopyConfig holds the configuration for copying configmap.
type ConfigMapCopyConfig struct {
	// SourceClient is the client to read the source configmap from.
	SourceClient client.Client
	// SourceNamespace is the namespace of the source configmap.
	SourceNamespace string
	// TargetNamespace is the namespace of the target configmap.
	TargetNamespace string
	// TargetName is an optional value to adjust the name of the target configmap
	// instead of using the source configmap name.
	TargetName string
}

// ManageCaConfigMap syncs the ca configmap to the target cluster.
func ManageCaConfigMap(targetCluster resources.ManagedCluster, caConfigMap corev1.LocalObjectReference, config ConfigMapCopyConfig) {
	caConfigMapName := caConfigMap.Name
	if config.TargetName != "" {
		caConfigMapName = config.TargetName
	}
	configMap := resources.NewManagedObject(&corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      caConfigMapName,
			Namespace: config.TargetNamespace,
		},
	}, resources.ManagedObjectContext{
		ReconcileFunc: func(ctx context.Context, o client.Object) error {
			oConfigMap, ok := o.(*corev1.ConfigMap)
			if !ok {
				return fmt.Errorf("expected *corev1.ConfigMap, got %T", o)
			}
			sourceConfigMap := &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      caConfigMap.Name,
					Namespace: config.SourceNamespace,
				},
			}
			// retrieve source configmap from platform cluster
			if err := config.SourceClient.Get(ctx, client.ObjectKeyFromObject(sourceConfigMap), sourceConfigMap); err != nil {
				return err
			}
			mutator := openmcpresources.NewConfigMapMutator(caConfigMapName, config.TargetNamespace, sourceConfigMap.Data)
			return mutator.Mutate(oConfigMap)
		},
		StatusFunc: ConfigMapStatus,
	})
	targetCluster.AddObject(configMap)
}

// ConfigMapStatus returns the status of a configmap object.
func ConfigMapStatus(o client.Object, rl apiv1alpha1.ResourceLocation) resources.Status {
	if !o.GetDeletionTimestamp().IsZero() {
		return resources.Status{
			Phase:    apiv1alpha1.Terminating,
			Message:  "ConfigMap is terminating.",
			Location: rl,
		}
	}
	if o.GetUID() == "" {
		return resources.Status{
			Phase:    apiv1alpha1.Pending,
			Message:  "ConfigMap has not been created yet.",
			Location: rl,
		}
	}
	return resources.Status{
		Phase:    apiv1alpha1.Ready,
		Message:  "ConfigMap exists.",
		Location: rl,
	}
}

var _ resources.OrphanCleaner = &configMapCleaner{}

type configMapCleaner struct {
	cluster          resources.ManagedCluster
	namespace        string
	configMapsToKeep []corev1.LocalObjectReference
}

// NewConfigMapCleaner removes redundant configmaps in the given target namespace
// by removing any configmap labeled as managed by service-provider-metrics-operator that is not in configMapsToKeep.
func NewConfigMapCleaner(cluster resources.ManagedCluster, namespace string, configMapsToKeep []corev1.LocalObjectReference) resources.OrphanCleaner {
	return &configMapCleaner{
		cluster:          cluster,
		namespace:        namespace,
		configMapsToKeep: configMapsToKeep,
	}
}

func (c *configMapCleaner) Cleanup(ctx context.Context) ([]resources.Result, error) {
	results := []resources.Result{}
	configMapCopies := &corev1.ConfigMapList{}
	cl := c.cluster.GetClient()
	if err := cl.List(ctx, configMapCopies,
		client.InNamespace(c.namespace),
		client.MatchingLabels{meta.LabelManagedBy: meta.LabelManagedByValue},
	); err != nil {
		log.FromContext(ctx).Error(err, "failed to list configmap for orphan cleanup")
		return nil, ErrConfigMapCleanup
	}
	for _, configMap := range configMapCopies.Items {
		if !slices.ContainsFunc(c.configMapsToKeep, func(ref corev1.LocalObjectReference) bool { return configMap.Name == ref.Name }) {
			if err := cl.Delete(ctx, &configMap); client.IgnoreNotFound(err) != nil {
				results = append(results, c.cleanupErrorResult(&configMap, err))
			}
		}
	}
	return results, nil
}

func (c *configMapCleaner) cleanupErrorResult(obj *corev1.ConfigMap, err error) resources.Result {
	return resources.Result{
		Object: resources.NewManagedObject(
			obj,
			resources.ManagedObjectContext{
				StatusFunc:     cleanupErrorStatusConfigMap,
				DeletionPolicy: resources.Delete,
			}),
		Cluster:         c.cluster,
		OperationResult: resources.OperationResultDeletionFailed,
		Error:           err,
	}
}

func cleanupErrorStatusConfigMap(_ client.Object, rl apiv1alpha1.ResourceLocation) resources.Status {
	return resources.Status{
		Phase:    apiv1alpha1.Terminating,
		Message:  "ConfigMap cleanup failed",
		Location: rl,
	}
}
