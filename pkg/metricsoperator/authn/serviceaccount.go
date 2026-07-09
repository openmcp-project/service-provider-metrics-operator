package authn

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/resources"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

const (
	serviceAccountMountPath = "/var/run/secrets/kubernetes.io/serviceaccount"
	serviceAccountVolume    = "kube-api-access"

	annotationTokenExpirationTime = "metrics-operator.services.openmcp.cloud/token-expiration-time"
)

var (
	errSANameOrNamespaceEmpty = errors.New("name or namespace in service account reference must not be empty")
	errRestConfigNil          = errors.New("rest config must not be nil")
	errExpirationInvalid      = errors.New("must not specify a duration less than 10 minutes")
)

type serviceAccountToken struct {
	CAData      []byte
	Token       string
	TokenExpiry time.Time
}

// ManagedServiceAccount references the managed ServiceAccount object
type ManagedServiceAccount struct {
	types.NamespacedName
}

func (m *ManagedServiceAccount) KubeAPIAccess() string {
	return fmt.Sprintf("kube-api-access-%s", m.Name)
}

// generateToken generates a token for the given ServiceAccount.
func generateToken(ctx context.Context, mcp *clusters.Cluster, cfg *rest.Config, svcAccRef types.NamespacedName, expiration time.Duration) (*serviceAccountToken, error) {
	if svcAccRef.Name == "" || svcAccRef.Namespace == "" {
		return nil, errSANameOrNamespaceEmpty
	}
	if cfg == nil {
		return nil, errRestConfigNil
	}
	if expiration < 10*time.Minute {
		return nil, errExpirationInvalid
	}

	sa := &corev1.ServiceAccount{}
	if err := mcp.Client().Get(ctx, types.NamespacedName{Name: svcAccRef.Name, Namespace: svcAccRef.Namespace}, sa); err != nil {
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

	rc := &serviceAccountToken{
		Token:       req.Status.Token,
		TokenExpiry: req.Status.ExpirationTimestamp.Time,
		CAData:      cfg.CAData,
	}

	if cfg.CAFile != "" {
		caBytes, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, err
		}
		rc.CAData = caBytes
	}

	return rc, nil
}

// Configure adds a managed ServiceAccount object to the given MCP cluster and a managed Secret object to the given workload cluster.
func (m *ManagedServiceAccount) Configure(workloadCluster, mcpCluster resources.ManagedCluster, values *apiextensionsv1.JSON, pollInterval time.Duration) {
	// Add a service account on the MCP cluster.
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
		},
	}
	msa := resources.NewManagedObject(sa, resources.ManagedObjectContext{
		ReconcileFunc: resources.NoOp,
		StatusFunc:    resources.SimpleStatus,
	})
	mcpCluster.AddObject(msa)

	// Add a secret on the workload cluster that contains a token for the MCP service account.
	secret := resources.NewManagedObject(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.KubeAPIAccess(),
			Namespace: workloadCluster.GetDefaultNamespace(),
		},
	}, resources.ManagedObjectContext{
		DependsOn: []resources.ManagedObject{
			msa,
		},
		ReconcileFunc: func(ctx context.Context, o client.Object) error {
			oSecret := o.(*corev1.Secret)

			// prevent token generation on every reconcile but at the same time consider the
			// provider config poll interval and an additional safeguard of a minute (pod volume refresh delay)
			nextReconcile := time.Now().Add(pollInterval).Add(time.Minute)
			expirationTime, err := getTokenExpirationTime(oSecret)
			if err != nil || expirationTime.Before(nextReconcile) {
				rc, err := generateToken(ctx, mcpCluster.GetCluster(), mcpCluster.GetConfig(), m.NamespacedName, 1*time.Hour)
				if err != nil {
					return err
				}
				oSecret.Data = map[string][]byte{
					"token":     []byte(rc.Token),
					"namespace": []byte(mcpCluster.GetDefaultNamespace()),
					"ca.crt":    rc.CAData,
				}
				setTokenExpirationTime(oSecret, rc.TokenExpiry)
			}

			return nil
		},
		StatusFunc: resources.SimpleStatus,
	})
	workloadCluster.AddObject(secret)
}

func getTokenExpirationTime(obj *corev1.Secret) (time.Time, error) {
	if obj.Annotations == nil {
		return time.Time{}, errors.New("no expiration time set")
	}
	expirationTime := obj.Annotations[annotationTokenExpirationTime]
	return time.Parse(time.RFC3339, expirationTime)
}

func setTokenExpirationTime(obj *corev1.Secret, expTime time.Time) {
	if obj.Annotations == nil {
		obj.Annotations = map[string]string{}
	}
	obj.Annotations[annotationTokenExpirationTime] = expTime.Format(time.RFC3339)
}
