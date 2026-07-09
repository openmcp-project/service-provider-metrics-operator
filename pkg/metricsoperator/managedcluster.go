package metricsoperator

import (
	"strings"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// ClusterTypeManagedControlPlane indicates a managed control plane cluster.
	ClusterTypeManagedControlPlane ClusterType = "ManagedControlPlane"
	// ClusterTypePlatform indicates a platform cluster.
	ClusterTypePlatform ClusterType = "PlatformCluster"
	// ClusterTypeWorkload indicates a workload cluster.
	ClusterTypeWorkload ClusterType = "WorkloadCluster"
)

// ClusterType distinguishes between managed control plane, platform, and workload clusters.
type ClusterType string

// NewManagedCluster creates a new ManagedCluster instance.
func NewManagedCluster(c *clusters.Cluster, cfg *rest.Config, ns string, ct ClusterType) ManagedCluster {
	return &managedCluster{
		cluster:          c,
		cfg:              cfg,
		objects:          []ManagedObject{},
		defaultNamespace: ns,
		clusterType:      ct,
	}
}

// ManagedCluster holds a set of ManagedObjects.
type ManagedCluster interface {
	AddObject(o ManagedObject)
	GetObjects() []ManagedObject
	GetDefaultNamespace() string
	GetHostAndPort() (string, string)
	GetConfig() *rest.Config
	GetClient() client.Client
	GetCluster() *clusters.Cluster
	GetClusterType() ClusterType
}

var _ ManagedCluster = &managedCluster{}

type managedCluster struct {
	cluster          *clusters.Cluster
	cfg              *rest.Config
	objects          []ManagedObject
	defaultNamespace string
	clusterType      ClusterType
}

func (m *managedCluster) GetClient() client.Client {
	return m.cluster.Client()
}

func (m *managedCluster) GetConfig() *rest.Config {
	return m.cfg
}

func (m *managedCluster) GetHostAndPort() (string, string) {
	input := strings.TrimPrefix(m.cfg.Host, "https://")
	host, port, found := strings.Cut(input, ":")
	if !found {
		port = "443"
	}
	return host, port
}

func (m *managedCluster) GetDefaultNamespace() string {
	return m.defaultNamespace
}

func (m *managedCluster) AddObject(o ManagedObject) {
	m.objects = append(m.objects, o)
}

func (m *managedCluster) GetObjects() []ManagedObject {
	return m.objects
}

func (m *managedCluster) GetCluster() *clusters.Cluster {
	return m.cluster
}

func (m *managedCluster) GetClusterType() ClusterType {
	return m.clusterType
}
