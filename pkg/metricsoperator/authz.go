package metricsoperator

import (
	"context"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const mcpClusterRoleBindingName = "metrics-operator-server"

// ConfigureAuthz adds a ClusterRoleBinding on the MCP cluster granting cluster-admin to the SA.
func ConfigureAuthz(mcpCluster ManagedCluster, msa *ManagedServiceAccount) {
	crb := NewManagedObject(&rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: mcpClusterRoleBindingName,
		},
	}, ManagedObjectContext{
		ReconcileFunc: func(_ context.Context, o client.Object) error {
			oCRB := o.(*rbacv1.ClusterRoleBinding)
			oCRB.Subjects = []rbacv1.Subject{
				{
					Kind:      rbacv1.ServiceAccountKind,
					Name:      msa.Name,
					Namespace: msa.Namespace,
				},
			}

			// TODO: define minimum set of permission the service provider requires on the mcp cluster
			oCRB.RoleRef = rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     "cluster-admin",
			}
			return nil
		},
		StatusFunc: SimpleStatus,
	})
	mcpCluster.AddObject(crb)
}
