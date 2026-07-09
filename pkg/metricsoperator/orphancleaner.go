package metricsoperator

import (
	"context"
	"errors"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/openmcp-project/service-provider-metrics-operator/api/v1alpha1"
	mmeta "github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/meta"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/resources"
)

// ErrOrphanCleanup is an user-facing error that indicates orphan cleanup failures
var ErrOrphanCleanup = errors.New("orphan cleanup failed")

var _ resources.OrphanCleaner = &orphanCleaner[*corev1.SecretList]{}

type orphanCleaner[T client.ObjectList] struct {
	cluster     resources.ManagedCluster
	namespace   string
	cleanerType cleanerType[T]
}

type cleanerType[T client.ObjectList] struct {
	ObjectsToKeep []corev1.LocalObjectReference
	EmptyList     func() T
	// PreDeletionSteps is an optional hook that is invoked before OrphanCleaner.Cleanup deletes an object.
	PreDeletionSteps func(context.Context, client.Object) (proceedWithDeletion bool, _ error)
}

// NewOrphanCleaner removes redundant objects in the given target namespace.
func NewOrphanCleaner[T client.ObjectList](cluster resources.ManagedCluster, namespace string, clType cleanerType[T]) resources.OrphanCleaner {
	return &orphanCleaner[T]{
		cluster:     cluster,
		namespace:   namespace,
		cleanerType: clType,
	}
}

func (c *orphanCleaner[T]) items(list T) []client.Object {
	items, _ := meta.ExtractList(list)
	objList := make([]client.Object, 0, len(items))
	for _, item := range items {
		if obj, ok := item.(client.Object); ok {
			objList = append(objList, obj)
		}
	}
	return objList
}

func (c *orphanCleaner[T]) Cleanup(ctx context.Context) ([]resources.Result, error) {
	results := []resources.Result{}
	if c.cleanerType.EmptyList == nil {
		return nil, fmt.Errorf("%w: orphan cleaner is missing empty list definition", ErrOrphanCleanup)
	}
	objList := c.cleanerType.EmptyList()
	cl := c.cluster.GetClient()
	if err := cl.List(ctx, objList,
		client.InNamespace(c.namespace),
		client.MatchingLabels{mmeta.LabelManagedBy: mmeta.LabelManagedByValue},
	); err != nil {
		log.FromContext(ctx).Error(err, "failed to list objects for orphan cleanup")
		return nil, ErrOrphanCleanup
	}
	for _, obj := range c.items(objList) {
		if !slices.ContainsFunc(c.cleanerType.ObjectsToKeep, func(ref corev1.LocalObjectReference) bool { return obj.GetName() == ref.Name }) {
			if c.cleanerType.PreDeletionSteps != nil {
				proceedWithDeletion, err := c.cleanerType.PreDeletionSteps(ctx, obj)
				if err != nil {
					results = append(results, c.deletionError(obj, err))
					continue
				}
				if !proceedWithDeletion {
					results = append(results, c.deletionPrepared(obj))
					continue
				}
			}
			if err := cl.Delete(ctx, obj); client.IgnoreNotFound(err) != nil {
				results = append(results, c.deletionError(obj, err))
			}
		}
	}
	return results, nil
}

func (c *orphanCleaner[T]) deletionPrepared(obj client.Object) resources.Result {
	moc := resources.ManagedObjectContext{
		ReconcileFunc:  nil,
		DependsOn:      nil,
		StatusFunc:     cleanupPreparedStatus,
		DeletionPolicy: resources.Delete,
	}
	mo := resources.NewManagedObject(obj, moc)

	return resources.Result{
		Object:          mo,
		Cluster:         c.cluster,
		OperationResult: resources.OperationResultDeletionRequested,
	}
}

func cleanupPreparedStatus(_ client.Object, rl apiv1alpha1.ResourceLocation) resources.Status {
	return resources.Status{
		Phase:    apiv1alpha1.Terminating,
		Message:  "Orphan cleanup prepared",
		Location: rl,
	}
}

func (c *orphanCleaner[T]) deletionError(obj client.Object, err error) resources.Result {
	moc := resources.ManagedObjectContext{
		ReconcileFunc:  nil,
		DependsOn:      nil,
		StatusFunc:     cleanupErrorStatus,
		DeletionPolicy: resources.Delete,
	}
	mo := resources.NewManagedObject(obj, moc)
	return resources.Result{
		Object:          mo,
		Cluster:         c.cluster,
		OperationResult: resources.OperationResultDeletionFailed,
		Error:           err,
	}
}

func cleanupErrorStatus(_ client.Object, rl apiv1alpha1.ResourceLocation) resources.Status {
	return resources.Status{
		Phase:    apiv1alpha1.Terminating,
		Message:  "Orphan cleanup failed",
		Location: rl,
	}
}
