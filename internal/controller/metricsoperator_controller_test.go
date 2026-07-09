package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apiv1alpha1 "github.com/openmcp-project/service-provider-metrics-operator/api/v1alpha1"
)

func TestSelectVersion(t *testing.T) {
	pc := &apiv1alpha1.ProviderConfig{
		Spec: apiv1alpha1.ProviderConfigSpec{
			Versions: []apiv1alpha1.MetricsOperatorVersion{
				{Version: "v1.0.0", ChartVersion: "1.0.0", ChartURL: new("oci://example.com/chart")},
				{Version: "v1.1.0", ChartVersion: "1.1.0", ChartURL: new("oci://example.com/chart")},
			},
		},
	}

	t.Run("found", func(t *testing.T) {
		v, err := selectVersion("v1.0.0", pc)
		require.NoError(t, err)
		assert.Equal(t, "v1.0.0", v.Version)
		assert.Equal(t, "1.0.0", v.ChartVersion)
	})

	t.Run("not found returns invalid user input error", func(t *testing.T) {
		_, err := selectVersion("v9.9.9", pc)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "v9.9.9")
	})
}
