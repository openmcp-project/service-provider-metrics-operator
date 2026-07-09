package resources

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/meta"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/objectutils"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	// OperationResultDeletionFailed indicates failed to be deleted
	OperationResultDeletionFailed controllerutil.OperationResult = "deletionFailed"
	// OperationResultDeletionRequested indicates that an object has been marked for deletion
	OperationResultDeletionRequested controllerutil.OperationResult = "deletionRequested"
	// OperationResultDeleted indicates that an object has been deleted
	OperationResultDeleted controllerutil.OperationResult = "deleted"
	// OperationResultOrphaned indicates that an object has been orphaned
	OperationResultOrphaned controllerutil.OperationResult = OperationResultDeleted
)

type dependents map[ManagedObject][]dependency

// Manager manages the objects of an arbitrary number of clusters
type Manager interface {
	AddCluster(mc ManagedCluster)
	AddCleaner(oc OrphanCleaner)
	Apply(context.Context) (_ []Result, cleanup error)
	Delete(context.Context) (_ []Result, cleanup error)
}

// OrphanCleaner removes any previously managed objects that are no longer part of the desired state.
type OrphanCleaner interface {
	Cleanup(ctx context.Context) ([]Result, error)
}

// NewManager creates a new Manager instance.
func NewManager() Manager {
	return &managerImpl{
		clusters: []ManagedCluster{},
		cleaners: []OrphanCleaner{},
	}
}

type managerImpl struct {
	clusters []ManagedCluster
	cleaners []OrphanCleaner
}

func (m *managerImpl) AddCluster(mc ManagedCluster) {
	m.clusters = append(m.clusters, mc)
}

func (m *managerImpl) AddCleaner(cleaner OrphanCleaner) {
	m.cleaners = append(m.cleaners, cleaner)
}

func (m *managerImpl) Apply(ctx context.Context) ([]Result, error) {
	return m.reconcileObjects(ctx, false)
}

func (m *managerImpl) Delete(ctx context.Context) ([]Result, error) {
	return m.reconcileObjects(ctx, true)
}

func (m *managerImpl) reconcileObjects(ctx context.Context, isDeletion bool) ([]Result, error) {
	deps := m.getDependents()
	results := []Result{}
	for _, mc := range m.clusters {
		for _, mo := range mc.GetObjects() {
			result := m.reconcileObject(ctx, mc, mo, deps, isDeletion)
			results = append(results, result)
		}
	}
	for _, c := range m.cleaners {
		result, err := c.Cleanup(ctx)
		if err != nil {
			return results, err
		}
		results = slices.Concat(results, result)
	}
	return results, nil
}

func (m *managerImpl) reconcileObject(ctx context.Context, mc ManagedCluster, mo ManagedObject, deps dependents, isDeletion bool) Result {
	cl := mc.GetClient()
	obj := mo.GetObject()

	if isDeletion {
		if err := m.checkForDependents(ctx, deps[mo]); err != nil {
			return Result{Object: mo, Cluster: mc, OperationResult: controllerutil.OperationResultNone, Error: err}
		}
		if mo.GetDeletionPolicy() == Orphan {
			return Result{Object: mo, Cluster: mc, OperationResult: OperationResultOrphaned, Error: nil}
		}
		err := cl.Delete(ctx, obj)
		if apierrors.IsNotFound(err) {
			return Result{Object: mo, Cluster: mc, OperationResult: OperationResultDeleted, Error: nil}
		}
		return Result{Object: mo, Cluster: mc, OperationResult: OperationResultDeletionRequested, Error: err}
	}

	opResult, err := controllerutil.CreateOrUpdate(ctx, cl, obj, func() error {
		meta.SetManagedBy(obj)
		return mo.Reconcile(ctx)
	})
	return Result{Object: mo, Cluster: mc, OperationResult: opResult, Error: err}
}

func (m *managerImpl) checkForDependents(ctx context.Context, deps []dependency) error {
	errs := []error{}
	for _, dep := range deps {
		obj := dep.Object.GetObject()
		err := dep.Cluster.GetClient().Get(ctx, client.ObjectKeyFromObject(obj), obj)
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		errs = append(errs, fmt.Errorf("dependent object still exists: %s", objectutils.ObjectID(obj)))
	}
	return errors.Join(errs...)
}

func (m *managerImpl) getDependents() dependents {
	deps := dependents{}
	for _, mc := range m.clusters {
		for _, mo := range mc.GetObjects() {
			for _, dep := range mo.GetDependencies() {
				if deps[dep] == nil {
					deps[dep] = []dependency{}
				}
				deps[dep] = append(deps[dep], dependency{Object: mo, Cluster: mc})
			}
		}
	}
	return deps
}

// Result summarizes a reconciliation result.
type Result struct {
	Object          ManagedObject
	Cluster         ManagedCluster
	OperationResult controllerutil.OperationResult
	Error           error
}

type dependency struct {
	Object  ManagedObject
	Cluster ManagedCluster
}

// AllDeleted returns true if every item's operation result is OperationResultDeleted.
func AllDeleted(results []Result) bool {
	for _, r := range results {
		if r.OperationResult != OperationResultDeleted {
			return false
		}
	}
	return true
}
