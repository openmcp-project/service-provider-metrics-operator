package metricsoperator

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
)

// HelmValues defines the helm values explicitly processed during reconciliation.
// Note: metrics-operator uses top-level imagePullSecrets, not global.imagePullSecrets.
type HelmValues struct {
	NamespaceOverride string                        `json:"namespaceOverride,omitempty"`
	ImagePullSecrets  []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

// ExtractHelmValues extracts helm values required for processing.
func ExtractHelmValues(values *apiextensionsv1.JSON) (*HelmValues, error) {
	if values == nil || len(values.Raw) == 0 {
		return &HelmValues{}, nil
	}
	vals := &HelmValues{}
	if err := json.Unmarshal(values.Raw, vals); err != nil {
		return nil, err
	}
	return vals, nil
}
