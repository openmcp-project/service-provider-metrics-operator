package helm

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

func TestExtractHelmValues(t *testing.T) {
	tests := []struct {
		name    string
		values  *apiextensionsv1.JSON
		want    *HelmValues
		wantErr bool
	}{
		{
			name: "extract namespaceOverride",
			values: &apiextensionsv1.JSON{
				Raw: []byte(`{"namespaceOverride": "custom-ns"}`),
			},
			want:    &HelmValues{NamespaceOverride: "custom-ns"},
			wantErr: false,
		},
		{
			name: "extract top-level imagePullSecrets",
			values: &apiextensionsv1.JSON{
				Raw: []byte(`{"imagePullSecrets": [{"name": "my-secret"}]}`),
			},
			want: &HelmValues{
				Global: Global{
					ImagePullSecrets: []corev1.LocalObjectReference{{Name: "my-secret"}},
				},
			},
			wantErr: false,
		},
		{
			name: "ignore unknown values",
			values: &apiextensionsv1.JSON{
				Raw: []byte(`{"replicaCount": 2}`),
			},
			want:    &HelmValues{},
			wantErr: false,
		},
		{
			name:    "nil values returns empty",
			values:  nil,
			want:    &HelmValues{},
			wantErr: false,
		},
		{
			name: "invalid json returns error",
			values: &apiextensionsv1.JSON{
				Raw: []byte("not-json"),
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractHelmValues(tt.values)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAddDefaultHelmValues_SetsOperatorConfigNamespace(t *testing.T) {
	tests := []struct {
		name      string
		namespace string
		values    *apiextensionsv1.JSON
	}{
		{
			name:      "default metrics-operator namespace",
			namespace: "metrics-operator",
			values:    nil,
		},
		{
			name:      "custom namespace override",
			namespace: "custom-ns",
			values:    &apiextensionsv1.JSON{Raw: []byte(`{"namespaceOverride":"custom-ns"}`)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AddDefaultHelmValues(tt.values, tt.namespace)
			assert.NoError(t, err)

			var root map[string]json.RawMessage
			assert.NoError(t, json.Unmarshal(got.Raw, &root))

			// configNamespace top-level key
			var configNS string
			assert.NoError(t, json.Unmarshal(root["configNamespace"], &configNS))
			assert.Equal(t, tt.namespace, configNS)

			// OPERATOR_CONFIG_NAMESPACE in manager.extraEnv
			var manager map[string]json.RawMessage
			assert.NoError(t, json.Unmarshal(root["manager"], &manager))
			var envs []corev1.EnvVar
			assert.NoError(t, json.Unmarshal(manager["extraEnv"], &envs))
			found := false
			for _, e := range envs {
				if e.Name == "OPERATOR_CONFIG_NAMESPACE" {
					assert.Equal(t, tt.namespace, e.Value)
					found = true
				}
			}
			assert.True(t, found, "OPERATOR_CONFIG_NAMESPACE not found in manager.extraEnv")
		})
	}
}
