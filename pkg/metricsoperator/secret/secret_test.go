package secret

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	apiv1alpha1 "github.com/openmcp-project/service-provider-metrics-operator/api/v1alpha1"

	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/meta"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/resources"
	"github.com/openmcp-project/service-provider-metrics-operator/pkg/metricsoperator/testutils"
)

func TestManagePullSecrets(t *testing.T) {
	sourceSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pull-secret",
			Namespace: "source-ns",
		},
		Data: map[string][]byte{
			".dockerconfigjson": []byte(`{"auths":{}}`),
		},
		Type: corev1.SecretTypeDockerConfigJson,
	}
	fakeCluster := testutils.CreateTestClusterWithClient(t, "platform", sourceSecret)

	tests := []struct {
		name             string
		targetCluster    resources.ManagedCluster
		imagePullSecrets []corev1.LocalObjectReference
		config           SecretCopyConfig
	}{
		{
			name:          "syncs secret with correct type",
			targetCluster: resources.NewManagedCluster(fakeCluster, &rest.Config{}, "target-ns", resources.WorkloadCluster),
			imagePullSecrets: []corev1.LocalObjectReference{
				{Name: "test-pull-secret"},
			},
			config: SecretCopyConfig{
				SourceClient:    fakeCluster.Client(),
				SourceNamespace: "source-ns",
				TargetNamespace: "target-ns",
			},
		},
		{
			name:          "sync secret with target name adjustment",
			targetCluster: resources.NewManagedCluster(fakeCluster, &rest.Config{}, "target-ns", resources.WorkloadCluster),
			imagePullSecrets: []corev1.LocalObjectReference{
				{Name: "test-pull-secret"},
			},
			config: SecretCopyConfig{
				SourceClient:    fakeCluster.Client(),
				SourceNamespace: "source-ns",
				TargetNamespace: "target-ns",
				TargetName:      fmt.Sprintf("%s%s", secretNamePrefix, "test-pull-secret"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ManagePullSecrets(tt.targetCluster, tt.imagePullSecrets, tt.config)

			// Apply managed objects
			mgr := resources.NewManager()
			mgr.AddCluster(tt.targetCluster)
			results, gotErr := mgr.Apply(context.Background())
			require.NoError(t, gotErr)
			for _, r := range results {
				require.NoError(t, r.Error)
			}

			// Verify secret was synced with correct type
			for _, pullSecret := range tt.imagePullSecrets {
				targetSecret := &corev1.Secret{}
				targetSecretName := pullSecret.Name
				if tt.config.TargetName != "" {
					targetSecretName = tt.config.TargetName
				}
				err := fakeCluster.Client().Get(context.Background(), client.ObjectKey{
					Name:      targetSecretName,
					Namespace: tt.config.TargetNamespace,
				}, targetSecret)
				require.NoError(t, err)

				assert.Equal(t, sourceSecret.Data, targetSecret.Data)
				assert.Equal(t, corev1.SecretTypeDockerConfigJson, targetSecret.Type, "target secret should have the correct type")
			}
		})
	}
}

func TestPrefixSecretName(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"short name", "privateregcred"},
		{"long name truncated", strings.Repeat("a", 60)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PrefixSecretName(tt.input)
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(got, secretNamePrefix))
			assert.LessOrEqual(t, len(got), 63)
		})
	}
}

func Test_secretCleaner_Cleanup(t *testing.T) {
	tests := []struct {
		name            string // description of this test case
		cluster         resources.ManagedCluster
		targetNamespace string
		secretsToKeep   []corev1.LocalObjectReference
		want            []corev1.Secret
		wantResults     bool // []results indicate individual delete error
		wantErr         bool // error indicate general errors that are not related to individual objects
	}{
		{
			name:            "only managed secrets are deleted",
			targetNamespace: "test-ns",
			cluster: testutils.CreateFakeCluster(testutils.CreateFakeClient([]client.Object{
				testSecret("a", "test-ns", true),
				testSecret("b", "test-ns", false),
			})),
			secretsToKeep: []corev1.LocalObjectReference{},
			want: []corev1.Secret{
				*testSecret("b", "test-ns", false),
			},
			wantErr: false,
		},
		{
			name:            "secrets in other namespaces are not deleted",
			targetNamespace: "openmcp-system",
			cluster: testutils.CreateFakeCluster(testutils.CreateFakeClient([]client.Object{
				testSecret("a", "test-ns", true),
				testSecret("b", "test-ns", false),
			})),
			secretsToKeep: []corev1.LocalObjectReference{},
			want: []corev1.Secret{
				*testSecret("a", "test-ns", true),
				*testSecret("b", "test-ns", false),
			},
			wantErr: false,
		},
		{
			name:            "secrets to keep are not deleted",
			targetNamespace: "test-ns",
			cluster: testutils.CreateFakeCluster(testutils.CreateFakeClient([]client.Object{
				testSecret("a", "test-ns", true),
				testSecret("b", "test-ns", false),
			})),
			secretsToKeep: []corev1.LocalObjectReference{
				{
					Name: "a",
				},
			},
			want: []corev1.Secret{
				*testSecret("a", "test-ns", true),
				*testSecret("b", "test-ns", false),
			},
			wantErr: false,
		},
		{
			name:            "error is returned when list fails",
			cluster:         testutils.CreateFakeCluster(testutils.ListErrorClient{}),
			targetNamespace: "test-ns",
			secretsToKeep:   []corev1.LocalObjectReference{},
			want:            []corev1.Secret{},
			wantErr:         true,
		},
		{
			name: "error is returned when delete fails",
			cluster: testutils.CreateFakeCluster(testutils.DeleteErrorClient{
				FakeSecret: *testSecret("a", "test-ns", true),
			}),
			targetNamespace: "test-ns",
			secretsToKeep:   []corev1.LocalObjectReference{},
			want:            []corev1.Secret{},
			wantErr:         false,
			wantResults:     true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewSecretCleaner(tt.cluster, tt.targetNamespace, tt.secretsToKeep)
			results, gotErr := c.Cleanup(context.Background())
			if gotErr != nil {
				if !tt.wantErr {
					t.Errorf("Cleanup() failed: %v", gotErr)
				}
				return
			}
			if len(results) > 0 {
				if !tt.wantResults {
					t.Errorf("Cleanup() failed %v", results)
				}
				return
			}
			secretList := &corev1.SecretList{}
			require.NoError(t, tt.cluster.GetClient().List(context.Background(), secretList))
			for _, gotSecret := range secretList.Items {
				assert.True(t, slices.ContainsFunc(tt.want, func(s corev1.Secret) bool {
					return s.Name == gotSecret.Name && s.Namespace == gotSecret.Namespace
				}))
			}
		})
	}
}

func testSecret(name, namespace string, managedByMetricsOperator bool) *corev1.Secret {
	labels := map[string]string{}
	if managedByMetricsOperator {
		labels[meta.LabelManagedBy] = meta.LabelManagedByValue
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
	}
}

// TestSecretStatus tests the SecretStatus function
func TestSecretStatus(t *testing.T) {
	tests := []struct {
		name     string
		obj      client.Object
		rl       apiv1alpha1.ResourceLocation
		expected apiv1alpha1.InstancePhase
	}{
		{
			name: "secret with UID - ready",
			obj: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test-ns",
					UID:       "test-uid",
				},
			},
			rl:       apiv1alpha1.WorkloadCluster,
			expected: apiv1alpha1.Ready,
		},
		{
			name: "secret without UID - pending",
			obj: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test-ns",
				},
			},
			rl:       apiv1alpha1.WorkloadCluster,
			expected: apiv1alpha1.Pending,
		},
		{
			name: "secret being deleted - terminating",
			obj: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:              "test",
					Namespace:         "test-ns",
					UID:               "test-uid",
					DeletionTimestamp: &metav1.Time{Time: time.Now()},
					Finalizers:        []string{"test-finalizer"},
				},
			},
			rl:       apiv1alpha1.WorkloadCluster,
			expected: apiv1alpha1.Terminating,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status := SecretStatus(tt.obj, tt.rl)
			assert.Equal(t, tt.expected, status.Phase)
			assert.Equal(t, tt.rl, status.Location)
		})
	}
}
