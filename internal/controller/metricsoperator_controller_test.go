package controller

import (
	"context"
	"strings"
	"testing"

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

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/opencontrolplane-runtime/pkg/serviceprovider/clusteraccess"

	apiv1alpha1 "github.com/openmcp-project/service-provider-metrics-operator/api/v1alpha1"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/instance"
)

// onboardingScheme includes MetricsOperator so the fake onboarding client accepts it.
func onboardingScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = apiv1alpha1.AddToScheme(s)
	return s
}

// noMatchMapper hides listed Kinds from the REST mapper, simulating absent CRDs.
type noMatchMapper struct {
	meta.RESTMapper
	hidden map[string]bool
}

func (m *noMatchMapper) RESTMappings(gk schema.GroupKind, versions ...string) ([]*meta.RESTMapping, error) {
	if m.hidden[gk.Kind] {
		return nil, &meta.NoKindMatchError{GroupKind: gk}
	}
	return m.RESTMapper.RESTMappings(gk, versions...)
}

func mcpClientWith(objs ...client.Object) *clusters.Cluster {
	mapper := &noMatchMapper{
		RESTMapper: testrestmapper.TestOnlyStaticRESTMapper(runtime.NewScheme()),
		hidden:     map[string]bool{},
	}
	cl := fake.NewClientBuilder().WithRESTMapper(mapper).WithObjects(objs...).Build()
	return clusters.NewTestClusterFromClient("mcp", cl)
}

func mcpClientNoCRDs() *clusters.Cluster {
	mapper := &noMatchMapper{
		RESTMapper: testrestmapper.TestOnlyStaticRESTMapper(runtime.NewScheme()),
		hidden:     map[string]bool{"MetricList": true, "ManagedMetricList": true},
	}
	cl := fake.NewClientBuilder().WithRESTMapper(mapper).Build()
	return clusters.NewTestClusterFromClient("mcp", cl)
}

func metricOnMCP(ns, name string) client.Object {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(schema.GroupVersionKind{
		Group: "metrics.openmcp.cloud", Version: "v1alpha1", Kind: "Metric",
	})
	u.SetNamespace(ns)
	u.SetName(name)
	return u
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

	r := &MetricsOperatorReconciler{}

	mcp := mcpClientWith(metricOnMCP("default", "my-metric"))
	result, err := r.Delete(context.Background(), obj, &apiv1alpha1.ProviderConfig{}, clusteraccess.ClusterContext{
		MCPCluster: mcp,
	})

	require.NoError(t, err)
	assert.Greater(t, result.RequeueAfter.Seconds(), float64(0), "must requeue while CRs exist")
	// condition must name the blocking kind
	found := false
	for _, c := range obj.Status.Conditions {
		if strings.Contains(c.Message, "Metric") {
			found = true
		}
	}
	assert.True(t, found, "condition message must name blocking kind(s)")
}

func TestDelete_ProceedsWhenNoCRDsInstalled(t *testing.T) {
	// Guard passes (no CRDs) → createObjectManager is reached, which will error due to missing
	// platform/workload clusters. That error is fine — we only care the guard didn't block.
	obj := moWithInstanceID()
	onboarding := clusters.NewTestClusterFromClient("onboarding",
		fake.NewClientBuilder().WithRESTMapper(
			testrestmapper.TestOnlyStaticRESTMapper(onboardingScheme()),
		).WithScheme(onboardingScheme()).WithObjects(obj).Build(),
	)
	r := &MetricsOperatorReconciler{OnboardingCluster: onboarding}

	mcp := mcpClientNoCRDs()
	result, _ := r.Delete(context.Background(), obj, &apiv1alpha1.ProviderConfig{}, clusteraccess.ClusterContext{
		MCPCluster: mcp,
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

	mcp := mcpClientWith() // CRDs present but no CRs
	result, _ := r.Delete(context.Background(), obj, &apiv1alpha1.ProviderConfig{}, clusteraccess.ClusterContext{
		MCPCluster: mcp,
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
