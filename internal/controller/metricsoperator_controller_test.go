package controller

import (
	"context"
	"slices"
	"testing"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider"
	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider/clusteraccess"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/meta/testrestmapper"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	apiv1alpha1 "github.com/openmcp-project/service-provider-metrics-operator/api/v1alpha1"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/cpresources"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/instance"
)

// onboardingScheme includes MetricsOperator so the fake onboarding client accepts it.
func onboardingScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = apiv1alpha1.AddToScheme(s)
	return s
}

// hideCrdInterceptor hides listed Kinds from the REST mapper, simulating absent CRDs.
func hideCrdInterceptor(hiddenCRDs ...string) interceptor.Funcs {
	return interceptor.Funcs{
		List: func(ctx context.Context, client client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			gvk := list.GetObjectKind().GroupVersionKind()
			if slices.Contains(hiddenCRDs, gvk.Kind) {
				return &meta.NoKindMatchError{GroupKind: gvk.GroupKind()}
			}
			return client.List(ctx, list, opts...)
		},
	}
}

func cpClientWith(objs ...client.ObjectList) *clusters.Cluster {
	cl := fake.NewClientBuilder().WithLists(objs...).Build()
	return clusters.NewTestClusterFromClient("cp", cl)
}

func cpClientNoCRDs() *clusters.Cluster {
	cl := fake.NewClientBuilder().WithInterceptorFuncs(hideCrdInterceptor("MetricList", "ManagedMetricList")).Build()
	return clusters.NewTestClusterFromClient("cp", cl)
}

func onboardingClient(objs ...client.Object) *clusters.Cluster {
	onboardingRestMapper := testrestmapper.TestOnlyStaticRESTMapper(onboardingScheme())
	onboardingClient := fake.NewClientBuilder().WithRESTMapper(onboardingRestMapper).WithScheme(onboardingScheme()).WithObjects(objs...).Build()
	return clusters.NewTestClusterFromClient("onboarding", onboardingClient)
}

func metricOnCP(ns, name string) client.ObjectList {
	u := unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group: cpresources.MetricsGroup, Version: cpresources.MetricsVersion, Kind: "Metric",
	})
	u.SetNamespace(ns)
	u.SetName(name)
	return &unstructured.UnstructuredList{Items: []unstructured.Unstructured{u}}
}

// moWithInstanceID returns a MetricsOperator with an instance ID pre-set so that
// ensureInstanceID skips the Update call and avoids needing a real OnboardingCluster.
func moWithInstanceID() *apiv1alpha1.MetricsOperator {
	obj := &apiv1alpha1.MetricsOperator{}
	obj.Name = "test"
	obj.Namespace = "default"
	instance.SetID(obj, instance.GenerateID(obj))
	return obj
}

func TestDelete_BlockedWhileMetricCRsExist(t *testing.T) {
	obj := &apiv1alpha1.MetricsOperator{}
	obj.Name = "test"
	obj.Namespace = "default"

	r := &MetricsOperatorReconciler{OnboardingCluster: onboardingClient(obj)}

	cp := cpClientWith(metricOnCP("default", "my-metric"))
	result, err := r.Delete(context.Background(), obj, &apiv1alpha1.ProviderConfig{}, clusteraccess.ClusterContext{
		MCPCluster: cp,
	})

	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter.Seconds(), float64(0), "must requeue while CRs exist")

	// condition must name the blocking kind
	conditions := meta.FindStatusCondition(obj.Status.Conditions, serviceprovider.ServiceProviderConditionReady)
	assert.Equal(t, "waiting for user resources to be deleted: Metric", conditions.Message)
}

func TestDelete_ProceedsWhenNoCRDsInstalled(t *testing.T) {
	// Guard passes (no CRDs) → createObjectManager is reached, which will error due to missing
	// platform/workload clusters. That error is fine — we only care the guard didn't block.
	obj := moWithInstanceID()

	r := &MetricsOperatorReconciler{OnboardingCluster: onboardingClient()}

	cp := cpClientNoCRDs()
	result, _ := r.Delete(context.Background(), obj, &apiv1alpha1.ProviderConfig{}, clusteraccess.ClusterContext{
		MCPCluster: cp,
	})

	assert.Equal(t, ctrl.Result{}, result, "guard must not block when CRDs are absent")
}

func TestDelete_ProceedsWhenNoMetricCRs(t *testing.T) {
	obj := moWithInstanceID()
	onboarding := clusters.NewTestClusterFromClient("onboarding",
		fake.NewClientBuilder().WithRESTMapper(
			testrestmapper.TestOnlyStaticRESTMapper(onboardingScheme()),
		).WithScheme(onboardingScheme()).WithObjects(obj).Build(),
	)
	r := &MetricsOperatorReconciler{OnboardingCluster: onboarding}

	cp := cpClientWith() // CRDs present but no CRs
	result, _ := r.Delete(context.Background(), obj, &apiv1alpha1.ProviderConfig{}, clusteraccess.ClusterContext{
		MCPCluster: cp,
	})

	assert.Equal(t, ctrl.Result{}, result, "guard must not block when no CRs exist")
}

func TestSelectVersion(t *testing.T) {
	pc := &apiv1alpha1.ProviderConfig{
		Spec: apiv1alpha1.ProviderConfigSpec{
			Versions: []apiv1alpha1.MetricsOperatorVersion{
				{Version: "v1.0.0", ChartVersion: "1.0.0", ChartURL: new("oci://example.com/chart")},
				{Version: "v1.1.0", ChartVersion: "1.1.0", ChartURL: new("oci://example.com/chart")},
			},
		},
	}

	t.Run("found", func(t *testing.T) {
		v, err := selectMetricsOperatorVersion("v1.0.0", pc)
		require.NoError(t, err)
		assert.Equal(t, "v1.0.0", v.Version)
		assert.Equal(t, "1.0.0", v.ChartVersion)
	})

	t.Run("not found returns invalid user input error", func(t *testing.T) {
		_, err := selectMetricsOperatorVersion("v9.9.9", pc)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "v9.9.9")
	})
}
