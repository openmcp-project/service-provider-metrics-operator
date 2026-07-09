package metricsoperator

import (
	"context"
	"fmt"

	ctrlutils "github.com/openmcp-project/controller-utils/pkg/controller"
	openmcpresources "github.com/openmcp-project/controller-utils/pkg/resources"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/resources"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SecretCopyConfig holds the configuration for copying a secret.
type SecretCopyConfig struct {
	SourceClient    client.Client
	SourceNamespace string
	TargetNamespace string
	TargetName      string
}

const secretNamePrefix = "sp-metricsop-"

// ManagePullSecret syncs a pull secret to the target cluster.
func ManagePullSecret(targetCluster resources.ManagedCluster, pullSecret corev1.LocalObjectReference, config SecretCopyConfig) {
	secret := resources.NewManagedObject(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      config.TargetName,
			Namespace: config.TargetNamespace,
		},
	}, resources.ManagedObjectContext{
		ReconcileFunc: func(ctx context.Context, o client.Object) error {
			oSecret, ok := o.(*corev1.Secret)
			if !ok {
				return fmt.Errorf("expected *corev1.Secret, got %T", o)
			}
			sourceSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      pullSecret.Name,
					Namespace: config.SourceNamespace,
				},
			}
			if err := config.SourceClient.Get(ctx, client.ObjectKeyFromObject(sourceSecret), sourceSecret); err != nil {
				return err
			}
			mutator := openmcpresources.NewSecretMutator(config.TargetName, config.TargetNamespace, sourceSecret.Data, corev1.SecretTypeDockerConfigJson)
			return mutator.Mutate(oSecret)
		},
		StatusFunc: resources.SimpleStatus,
	})
	targetCluster.AddObject(secret)
}

// ManagePullSecrets syncs multiple pull secrets to the target cluster.
func ManagePullSecrets(targetCluster resources.ManagedCluster, pullSecrets []corev1.LocalObjectReference, config SecretCopyConfig) {
	for _, ps := range pullSecrets {
		cfg := config
		cfg.TargetName = ps.Name
		ManagePullSecret(targetCluster, ps, cfg)
	}
}

// PrefixSecretName adds a prefix to prevent name collisions.
func PrefixSecretName(secretName string) (string, error) {
	return ctrlutils.ShortenToXCharacters(fmt.Sprintf("%s%s", secretNamePrefix, secretName), ctrlutils.K8sMaxNameLength)
}

// NewSecretCleaner removes redundant pull secrets in the given target namespace.
func NewSecretCleaner(cluster resources.ManagedCluster, namespace string, secretsToKeep []corev1.LocalObjectReference) resources.OrphanCleaner {
	return NewOrphanCleaner(cluster, namespace, cleanerType[*corev1.SecretList]{
		EmptyList: func() *corev1.SecretList {
			return &corev1.SecretList{}
		},
		ObjectsToKeep: secretsToKeep,
	})
}
