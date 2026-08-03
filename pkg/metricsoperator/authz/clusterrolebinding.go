package authz

import (
	"context"

	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/authn"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/resources"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const clusterRoleBindingName = "metrics-operator-server"

// Configure adds a managed ClusterRoleBinding object to the given cluster.
// The passed in service account is granted the cluster-admin role.
func Configure(cluster resources.ManagedCluster, msa *authn.ManagedServiceAccount) {
	crb := resources.NewManagedObject(&rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterRoleBindingName,
		},
	}, resources.ManagedObjectContext{
		ReconcileFunc: func(_ context.Context, o client.Object) error {
			oCRB := o.(*rbacv1.ClusterRoleBinding)
			oCRB.Subjects = []rbacv1.Subject{
				{
					Kind:      rbacv1.ServiceAccountKind,
					Name:      msa.Name,
					Namespace: msa.Namespace,
				},
			}
			// TODO: define minimum set of permissions the service provider requires on the CP cluster
			oCRB.RoleRef = rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     "cluster-admin",
			}
			return nil
		},
		StatusFunc: resources.SimpleStatus,
	})
	cluster.AddObject(crb)
}
