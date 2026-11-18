package authn_test

import (
	"context"
	"testing"
	"time"

	"github.com/openmcp-project/controller-utils/pkg/clusters"
	"github.com/openmcp-project/extensibility-utils/pkg/tokenrefresh"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	authenticationv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/authn"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/resources"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/testutils"
)

const (
	saName = "metrics-operator-server"
	// saNamespace differs from cpDefaultNamespace (namespaceOverride case), so the test catches
	// the Secret's namespace key being taken from the wrong one.
	saNamespace        = "custom-ns"
	cpDefaultNamespace = "metrics-operator"
	workloadNamespace  = "metrics-operator-abc"
	secretName         = "kube-api-access-metrics-operator-server"
	expAnnotation      = "metrics-operator.services.open-control-plane.io/token-expiration-time"
	existingToken      = "existing-token"
	mintedToken        = "minted-token"
)

var caBundle = []byte("ca-bundle")

// cpClusterWithTokenIssuer builds a ControlPlane cluster with the ServiceAccount, whose fake client mints
// ServiceAccount tokens and whose REST config carries a CA bundle, so the initial token write in Configure can succeed.
func cpClusterWithTokenIssuer(t *testing.T) resources.ManagedCluster {
	t.Helper()
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: saName, Namespace: saNamespace}}
	cl := fake.NewClientBuilder().WithObjects(sa).WithInterceptorFuncs(interceptor.Funcs{
		SubResourceCreate: func(ctx context.Context, c client.Client, subResource string, obj client.Object, subResourceObj client.Object, opts ...client.SubResourceCreateOption) error {
			tr, ok := subResourceObj.(*authenticationv1.TokenRequest)
			if subResource != "token" || !ok {
				return c.SubResource(subResource).Create(ctx, obj, subResourceObj, opts...)
			}
			tr.Status = authenticationv1.TokenRequestStatus{
				Token:               mintedToken,
				ExpirationTimestamp: metav1.NewTime(time.Now().Add(time.Hour)),
			}
			return nil
		},
	}).Build()
	cfg := &rest.Config{TLSClientConfig: rest.TLSClientConfig{CAData: caBundle}}
	return resources.NewManagedCluster(clusters.NewTestClusterFromClient("cp", cl).WithRESTConfig(cfg), cfg, cpDefaultNamespace, resources.ControlPlane)
}

// tokenSecret returns the managed token Secret that Configure added to the workload cluster.
func tokenSecret(t *testing.T, workloadCluster resources.ManagedCluster) resources.ManagedObject {
	t.Helper()
	for _, o := range workloadCluster.GetObjects() {
		if _, ok := o.GetObject().(*corev1.Secret); ok {
			return o
		}
	}
	require.Fail(t, "token secret not configured")
	return nil
}

func TestManagedServiceAccount_Configure(t *testing.T) {
	existingExpirationTime := time.Now().Add(time.Hour).Format(time.RFC3339)
	tests := []struct {
		name string
		// existing is the token Secret content before the reconcile, nil for a new Secret.
		existing  *corev1.Secret
		wantToken string
	}{
		{
			name:      "new secret gets an initial token",
			wantToken: mintedToken,
		},
		{
			name: "existing token and expiration time are kept",
			existing: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{expAnnotation: existingExpirationTime}},
				Data: map[string][]byte{
					tokenrefresh.SecretKeyToken:     []byte(existingToken),
					tokenrefresh.SecretKeyNamespace: []byte(saNamespace),
				},
			},
			wantToken: existingToken,
		},
		{
			name: "existing token for a ServiceAccount in another namespace is re-minted",
			existing: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{expAnnotation: existingExpirationTime}},
				Data: map[string][]byte{
					tokenrefresh.SecretKeyToken:     []byte(existingToken),
					tokenrefresh.SecretKeyNamespace: []byte(cpDefaultNamespace),
				},
			},
			wantToken: mintedToken,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := &authn.ManagedServiceAccount{NamespacedName: types.NamespacedName{Name: saName, Namespace: saNamespace}}
			workloadCluster := resources.NewManagedCluster(testutils.CreateTestClusterWithClient(t, "workload"), &rest.Config{}, workloadNamespace, resources.WorkloadCluster)

			m.Configure(workloadCluster, cpClusterWithTokenIssuer(t))
			mo := tokenSecret(t, workloadCluster)
			secret := mo.GetObject().(*corev1.Secret)
			assert.Equal(t, types.NamespacedName{Name: secretName, Namespace: workloadNamespace}, client.ObjectKeyFromObject(secret))
			if tt.existing != nil {
				secret.Annotations = tt.existing.Annotations
				secret.Data = tt.existing.Data
			}

			require.NoError(t, mo.Reconcile(context.Background()))

			assert.Equal(t, tt.wantToken, string(secret.Data[tokenrefresh.SecretKeyToken]))
			if tt.wantToken == mintedToken {
				// the initial token is minted by Configure so the pod never mounts an empty volume;
				// the tokenrefresh controller only renews it afterwards
				assert.Equal(t, caBundle, secret.Data[tokenrefresh.SecretKeyCA])
				assert.Equal(t, saNamespace, string(secret.Data[tokenrefresh.SecretKeyNamespace]))
				assert.NotEmpty(t, secret.Annotations[expAnnotation])
				assert.NotEmpty(t, secret.Annotations[tokenrefresh.CreationAnnotation(authn.TokenServiceProvider)])
			} else {
				// an existing token must be kept: Configure must neither re-mint nor drop it
				assert.Equal(t, existingExpirationTime, secret.Annotations[expAnnotation])
			}
		})
	}
}

func TestManagedServiceAccount_TokenBinding(t *testing.T) {
	m := &authn.ManagedServiceAccount{NamespacedName: types.NamespacedName{Name: saName, Namespace: saNamespace}}
	binding := m.TokenBinding(workloadNamespace)
	assert.Equal(t, types.NamespacedName{Name: saName, Namespace: saNamespace}, binding.ServiceAccount)
	assert.Equal(t, types.NamespacedName{Name: secretName, Namespace: workloadNamespace}, binding.Secret)
	assert.Equal(t, expAnnotation, tokenrefresh.ExpirationAnnotation(authn.TokenServiceProvider))
}
