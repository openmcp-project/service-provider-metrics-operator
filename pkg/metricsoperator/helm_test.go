package metricsoperator

import (
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
				ImagePullSecrets: []corev1.LocalObjectReference{{Name: "my-secret"}},
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
