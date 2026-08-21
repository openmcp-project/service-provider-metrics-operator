package testutils

import (
	"context"
	"errors"
	"testing"

	helmv2 "github.com/fluxcd/helm-controller/api/v2"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/openmcp-project/controller-utils/pkg/clusters"
	clustersv1alpha1 "github.com/openmcp-project/openmcp-operator/api/clusters/v1alpha1"
	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openmcp-project/service-provider-metrics-operator/api/v1alpha1"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/resources"
)

// CreateFakeClusterWithClient sets up a cluster with a fake client
func CreateFakeClusterWithClient(t *testing.T, id string, clusterObjects ...client.Object) *clusters.Cluster {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = apiextv1.AddToScheme(scheme)
	_ = v1alpha1.AddToScheme(scheme)
	_ = clustersv1alpha1.AddToScheme(scheme)
	_ = helmv2.AddToScheme(scheme)
	_ = sourcev1.AddToScheme(scheme)

	// init cluster with objects
	fakeClient := fake.NewClientBuilder().WithObjects(clusterObjects...).WithScheme(scheme).Build()
	return clusters.NewTestClusterFromClient(id, fakeClient)
}

var _ resources.ManagedCluster = &fakeCluster{}

type fakeCluster struct {
	resources.ManagedCluster
	fakeClient client.Client
}

// GetClient implements [ManagedCluster].
func (f *fakeCluster) GetClient() client.Client {
	return f.fakeClient
}

// CreateFakeCluster creates a fake cluster with a client
func CreateFakeCluster(client client.Client) resources.ManagedCluster {
	return &fakeCluster{
		fakeClient: client,
	}
}

// CreateFakeClient creates a fake client
func CreateFakeClient(clusterObjects []client.Object) client.Client {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	return fake.NewClientBuilder().WithObjects(clusterObjects...).WithScheme(scheme).Build()
}

type ListErrorClient struct {
	client.Client
}

func (l ListErrorClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return errors.New("list failed")
}
