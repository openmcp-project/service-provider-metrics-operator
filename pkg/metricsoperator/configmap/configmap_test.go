package configmap

import (
	"context"
	"errors"
	"slices"
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

func TestManageCaConfigMap(t *testing.T) {
	sourceConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "custom-ca",
			Namespace: "source-ns",
		},
		Data: map[string]string{
			"ca.crt": "CERTDATA",
		},
	}
	fakeCluster := testutils.CreateTestClusterWithClient(t, "platform", sourceConfigMap)

	tests := []struct {
		name          string
		targetCluster resources.ManagedCluster
		caConfigMap   corev1.LocalObjectReference
		config        ConfigMapCopyConfig
	}{
		{
			name:          "syncs configmap",
			targetCluster: resources.NewManagedCluster(fakeCluster, &rest.Config{}, "target-ns", resources.WorkloadCluster),
			caConfigMap:   corev1.LocalObjectReference{Name: "custom-ca"},
			config: ConfigMapCopyConfig{
				SourceClient:    fakeCluster.Client(),
				SourceNamespace: "source-ns",
				TargetNamespace: "target-ns",
			},
		},
		{
			name:          "syncs configmap with target name adjustment",
			targetCluster: resources.NewManagedCluster(fakeCluster, &rest.Config{}, "target-ns", resources.WorkloadCluster),
			caConfigMap:   corev1.LocalObjectReference{Name: "custom-ca"},
			config: ConfigMapCopyConfig{
				SourceClient:    fakeCluster.Client(),
				SourceNamespace: "source-ns",
				TargetNamespace: "target-ns",
				TargetName:      "sp-flux-custom-ca",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ManageCaConfigMap(tt.targetCluster, tt.caConfigMap, tt.config)

			mgr := resources.NewManager()
			mgr.AddCluster(tt.targetCluster)
			results, gotErr := mgr.Apply(context.Background())
			require.NoError(t, gotErr)
			for _, r := range results {
				require.NoError(t, r.Error)
			}

			targetConfigMap := &corev1.ConfigMap{}
			targetName := tt.caConfigMap.Name
			if tt.config.TargetName != "" {
				targetName = tt.config.TargetName
			}
			err := fakeCluster.Client().Get(context.Background(), client.ObjectKey{
				Name:      targetName,
				Namespace: tt.config.TargetNamespace,
			}, targetConfigMap)
			require.NoError(t, err)
			assert.Equal(t, sourceConfigMap.Data, targetConfigMap.Data)
		})
	}
}

func Test_configMapCleaner_Cleanup(t *testing.T) {
	tests := []struct {
		name             string
		cluster          resources.ManagedCluster
		targetNamespace  string
		configMapsToKeep []corev1.LocalObjectReference
		want             []corev1.ConfigMap
		wantResults      bool
		wantErr          bool
	}{
		{
			name:            "only managed configmaps are deleted",
			targetNamespace: "flux-system",
			cluster: testutils.CreateFakeCluster(testutils.CreateFakeClient([]client.Object{
				testConfigMap("a", "flux-system", true),
				testConfigMap("b", "flux-system", false),
			})),
			configMapsToKeep: []corev1.LocalObjectReference{},
			want: []corev1.ConfigMap{
				*testConfigMap("b", "flux-system", false),
			},
			wantErr: false,
		},
		{
			name:            "configmaps in other namespaces are not deleted",
			targetNamespace: "openmcp-system",
			cluster: testutils.CreateFakeCluster(testutils.CreateFakeClient([]client.Object{
				testConfigMap("a", "flux-system", true),
				testConfigMap("b", "flux-system", false),
			})),
			configMapsToKeep: []corev1.LocalObjectReference{},
			want: []corev1.ConfigMap{
				*testConfigMap("a", "flux-system", true),
				*testConfigMap("b", "flux-system", false),
			},
			wantErr: false,
		},
		{
			name:            "configmaps to keep are not deleted",
			targetNamespace: "flux-system",
			cluster: testutils.CreateFakeCluster(testutils.CreateFakeClient([]client.Object{
				testConfigMap("a", "flux-system", true),
				testConfigMap("b", "flux-system", false),
			})),
			configMapsToKeep: []corev1.LocalObjectReference{
				{
					Name: "a",
				},
			},
			want: []corev1.ConfigMap{
				*testConfigMap("a", "flux-system", true),
				*testConfigMap("b", "flux-system", false),
			},
			wantErr: false,
		},
		{
			name:             "error is returned when list fails",
			cluster:          testutils.CreateFakeCluster(testutils.ListErrorClient{}),
			targetNamespace:  "flux-system",
			configMapsToKeep: []corev1.LocalObjectReference{},
			want:             []corev1.ConfigMap{},
			wantErr:          true,
		},
		{
			name: "error is returned when delete fails",
			cluster: testutils.CreateFakeCluster(deleteErrorConfigMapClient{
				fakeConfigMap: *testConfigMap("a", "flux-system", true),
			}),
			targetNamespace:  "flux-system",
			configMapsToKeep: []corev1.LocalObjectReference{},
			want:             []corev1.ConfigMap{},
			wantErr:          false,
			wantResults:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewConfigMapCleaner(tt.cluster, tt.targetNamespace, tt.configMapsToKeep)
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
			configMapList := &corev1.ConfigMapList{}
			require.NoError(t, tt.cluster.GetClient().List(context.Background(), configMapList))
			for _, gotConfigMap := range configMapList.Items {
				assert.True(t, slices.ContainsFunc(tt.want, func(cm corev1.ConfigMap) bool {
					return cm.Name == gotConfigMap.Name && cm.Namespace == gotConfigMap.Namespace
				}))
			}
		})
	}
}

func testConfigMap(name, namespace string, managedByFlux bool) *corev1.ConfigMap {
	labels := map[string]string{}
	if managedByFlux {
		labels[meta.LabelManagedBy] = meta.LabelManagedByValue
	}
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
	}
}

// TestConfigMapStatus tests the ConfigMapStatus function
func TestConfigMapStatus(t *testing.T) {
	tests := []struct {
		name     string
		obj      client.Object
		rl       apiv1alpha1.ResourceLocation
		expected apiv1alpha1.InstancePhase
	}{
		{
			name: "configmap with UID - ready",
			obj: &corev1.ConfigMap{
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
			name: "configmap without UID - pending",
			obj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test-ns",
				},
			},
			rl:       apiv1alpha1.WorkloadCluster,
			expected: apiv1alpha1.Pending,
		},
		{
			name: "configmap being deleted - terminating",
			obj: &corev1.ConfigMap{
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
			status := ConfigMapStatus(tt.obj, tt.rl)
			assert.Equal(t, tt.expected, status.Phase)
			assert.Equal(t, tt.rl, status.Location)
		})
	}
}

type deleteErrorConfigMapClient struct {
	client.Client
	fakeConfigMap corev1.ConfigMap
}

func (d deleteErrorConfigMapClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	configMapList := list.(*corev1.ConfigMapList)
	configMapList.Items = []corev1.ConfigMap{d.fakeConfigMap}
	return nil
}

func (d deleteErrorConfigMapClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return errors.New("delete failed")
}
