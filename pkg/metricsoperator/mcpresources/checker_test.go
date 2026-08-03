package mcpresources_test

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/mcpresources"
)

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

func newFakeClient(hidden []string, objs ...client.Object) client.Client {
	return fake.NewClientBuilder().WithInterceptorFuncs(hideCrdInterceptor(hidden...)).WithObjects(objs...).Build()
}

func metricObj(ns, name string) client.Object {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: mcpresources.MetricsGroup, Version: mcpresources.MetricsVersion, Kind: "Metric"})
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func managedMetricObj(ns, name string) client.Object {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: mcpresources.MetricsGroup, Version: mcpresources.MetricsVersion, Kind: "ManagedMetric"})
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func TestBlockingKinds_NoCRDsInstalled(t *testing.T) {
	cl := newFakeClient([]string{"MetricList", "ManagedMetricList"})

	kinds, err := mcpresources.BlockingKinds(context.Background(), cl)
	require.NoError(t, err, "NoKindMatchError should be swallowed")
	assert.Empty(t, kinds)
}

func TestBlockingKinds_Empty(t *testing.T) {
	cl := newFakeClient(nil)

	kinds, err := mcpresources.BlockingKinds(context.Background(), cl)
	require.NoError(t, err)
	assert.Empty(t, kinds)
}

func TestBlockingKinds_MetricExists(t *testing.T) {
	cl := newFakeClient(nil, metricObj("default", "my-metric"))

	kinds, err := mcpresources.BlockingKinds(context.Background(), cl)
	require.NoError(t, err)
	assert.Equal(t, []string{"Metric"}, kinds)
}

func TestBlockingKinds_ManagedMetricExists(t *testing.T) {
	cl := newFakeClient(nil, managedMetricObj("default", "my-managed-metric"))

	kinds, err := mcpresources.BlockingKinds(context.Background(), cl)
	require.NoError(t, err)
	assert.Equal(t, []string{"ManagedMetric"}, kinds)
}

func TestBlockingKinds_PartialNoMatchStillChecksOther(t *testing.T) {
	cl := newFakeClient([]string{"MetricList"}, managedMetricObj("default", "my-managed-metric"))

	kinds, err := mcpresources.BlockingKinds(context.Background(), cl)
	require.NoError(t, err)
	assert.Equal(t, []string{"ManagedMetric"}, kinds)
}
