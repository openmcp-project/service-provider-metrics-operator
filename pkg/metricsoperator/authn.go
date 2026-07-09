package metricsoperator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	annotationTokenExpirationTime = "metrics-operator.services.openmcp.cloud/token-expiration-time"
	serviceAccountVolume          = "kube-api-access"
	serviceAccountMountPath       = "/var/run/secrets/kubernetes.io/serviceaccount"
)

// ManagedServiceAccount references the ServiceAccount managed on the MCP cluster
// and the corresponding token Secret on the workload cluster.
type ManagedServiceAccount struct {
	types.NamespacedName
}

// KubeAPIAccessSecretName returns the name of the token Secret on the workload cluster.
func (m *ManagedServiceAccount) KubeAPIAccessSecretName() string {
	return fmt.Sprintf("kube-api-access-%s", m.Name)
}

// Configure adds a ServiceAccount to the MCP cluster and a token Secret to the workload cluster.
func (m *ManagedServiceAccount) Configure(workloadCluster, mcpCluster ManagedCluster, pollInterval time.Duration) {
	sa := NewManagedObject(&corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
	}, ManagedObjectContext{
		ReconcileFunc: NoOp,
		StatusFunc:    SimpleStatus,
	})
	mcpCluster.AddObject(sa)

	secret := NewManagedObject(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.KubeAPIAccessSecretName(),
			Namespace: workloadCluster.GetDefaultNamespace(),
		},
	}, ManagedObjectContext{
		DependsOn: []ManagedObject{sa},
		ReconcileFunc: func(ctx context.Context, o client.Object) error {
			oSecret := o.(*corev1.Secret)

			// refresh token before it expires relative to the next poll cycle
			nextReconcile := time.Now().Add(pollInterval).Add(time.Minute)
			expirationTime, err := getTokenExpirationTime(oSecret)
			if err != nil || expirationTime.Before(nextReconcile) {
				tok, err := generateToken(ctx, mcpCluster.GetCluster(), mcpCluster.GetConfig(), m.NamespacedName, 1*time.Hour)
				if err != nil {
					return err
				}
				oSecret.Data = map[string][]byte{
					"token":     []byte(tok.token),
					"namespace": []byte(m.Namespace),
					"ca.crt":    tok.caData,
				}
				setTokenExpirationTime(oSecret, tok.expiry)
			}
			return nil
		},
		StatusFunc: SimpleStatus,
	})
	workloadCluster.AddObject(secret)
}

type saToken struct {
	token  string
	caData []byte
	expiry time.Time
}

func generateToken(ctx context.Context, mcp *clusters.Cluster, cfg *rest.Config, ref types.NamespacedName, expiration time.Duration) (*saToken, error) {
	if ref.Name == "" || ref.Namespace == "" {
		return nil, errors.New("service account name and namespace must not be empty")
	}
	if cfg == nil {
		return nil, errors.New("rest config must not be nil")
	}
	if expiration < 10*time.Minute {
		return nil, errors.New("token expiration must be at least 10 minutes")
	}

	sa := &corev1.ServiceAccount{}
	if err := mcp.Client().Get(ctx, ref, sa); err != nil {
		return nil, err
	}

	req := &authenticationv1.TokenRequest{
		Spec: authenticationv1.TokenRequestSpec{
			ExpirationSeconds: ptr.To(int64(expiration.Seconds())),
		},
	}
	if err := mcp.Client().SubResource("token").Create(ctx, sa, req); err != nil {
		return nil, err
	}

	caData := cfg.CAData
	if cfg.CAFile != "" {
		var err error
		caData, err = os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
	}

	return &saToken{
		token:  req.Status.Token,
		caData: caData,
		expiry: req.Status.ExpirationTimestamp.Time,
	}, nil
}

func getTokenExpirationTime(secret *corev1.Secret) (time.Time, error) {
	if secret.Annotations == nil {
		return time.Time{}, errors.New("no expiration annotation")
	}
	return time.Parse(time.RFC3339, secret.Annotations[annotationTokenExpirationTime])
}

func setTokenExpirationTime(secret *corev1.Secret, t time.Time) {
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	secret.Annotations[annotationTokenExpirationTime] = t.Format(time.RFC3339)
}
