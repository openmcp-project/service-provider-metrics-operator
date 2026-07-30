package mcpresources_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/meta/testrestmapper"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/mcpresources"
)

type noMatchMapper struct {
	apimeta.RESTMapper
	hiddenKinds map[string]bool
}

func (m *noMatchMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*apimeta.RESTMapping, error) {
	if m.hiddenKinds[gk.Kind] {
		return nil, &apimeta.NoKindMatchError{GroupKind: gk}
	}
	return m.RESTMapper.RESTMappings(gk, versions...)
}

func newFakeClient(hidden map[string]bool, objs ...client.Object) client.Client {
	mapper := &noMatchMapper{
		RESTMapper:  testrestmapper.TestOnlyStaticRESTMapper(scheme.Scheme),
		hiddenKinds: hidden,
	}
	return fake.NewClientBuilder().WithRESTMapper(mapper).WithObjects(objs...).Build()
}

func metricObj(ns, name string) client.Object {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "metrics.openmcp.cloud", Version: "v1alpha1", Kind: "Metric"})
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func managedMetricObj(ns, name string) client.Object {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{Group: "metrics.openmcp.cloud", Version: "v1alpha1", Kind: "ManagedMetric"})
	u.SetNamespace(ns)
	u.SetName(name)
	return u
}

func TestBlockingKinds_NoCRDsInstalled(t *testing.T) {
	cl := newFakeClient(map[string]bool{"MetricList": true, "ManagedMetricList": true})

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
	cl := newFakeClient(
		map[string]bool{"MetricList": true},
		managedMetricObj("default", "my-managed-metric"),
	)

	kinds, err := mcpresources.BlockingKinds(context.Background(), cl)
	require.NoError(t, err)
	assert.Equal(t, []string{"ManagedMetric"}, kinds)
}
