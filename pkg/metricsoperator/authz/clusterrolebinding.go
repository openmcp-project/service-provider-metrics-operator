package authz

import (
	"context"

	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/authn"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/cpresources"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/resources"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const clusterRoleName = "metrics-operator-server"

// Configure adds managed RBAC objects to the given cluster.
// The passed in service account is granted the permissions required by the metrics operator.
func Configure(cluster resources.ManagedCluster, msa *authn.ManagedServiceAccount) {
	cr := resources.NewManagedObject(&rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterRoleName,
		},
	}, resources.ManagedObjectContext{
		ReconcileFunc: func(_ context.Context, o client.Object) error {
			oCR := o.(*rbacv1.ClusterRole)
			oCR.Rules = []rbacv1.PolicyRule{
				{
					APIGroups: []string{"apiextensions.k8s.io"},
					Resources: []string{"customresourcedefinitions"},
					Verbs:     []string{"*"},
				},
				{
					APIGroups: []string{cpresources.MetricsGroup},
					Resources: []string{
						"metrics",
						"metrics/status",
						"managedmetrics",
						"managedmetrics/status",
						"federatedmetrics",
						"federatedmetrics/status",
						"federatedmanagedmetrics",
						"federatedmanagedmetrics/status",
					},
					Verbs: []string{"*"},
				},
				{
					APIGroups: []string{""},
					Resources: []string{"secrets"},
					Verbs:     []string{"get", "list", "watch"},
				},
				{
					APIGroups: []string{"events.k8s.io"},
					Resources: []string{"events"},
					Verbs:     []string{"create", "patch"},
				},
				{
					APIGroups: []string{"*"},
					Resources: []string{"*"},
					Verbs:     []string{"get", "list", "watch"},
				},
			}
			return nil
		},
		StatusFunc: resources.SimpleStatus,
	})
	cluster.AddObject(cr)

	crb := resources.NewManagedObject(&rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: clusterRoleName,
		},
	}, resources.ManagedObjectContext{
		DependsOn: []resources.ManagedObject{cr},
		ReconcileFunc: func(_ context.Context, o client.Object) error {
			oCRB := o.(*rbacv1.ClusterRoleBinding)
			oCRB.Subjects = []rbacv1.Subject{
				{
					Kind:      rbacv1.ServiceAccountKind,
					Name:      msa.Name,
					Namespace: msa.Namespace,
				},
			}
			oCRB.RoleRef = rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     clusterRoleName,
			}
			return nil
		},
		StatusFunc: resources.SimpleStatus,
	})
	cluster.AddObject(crb)
}
