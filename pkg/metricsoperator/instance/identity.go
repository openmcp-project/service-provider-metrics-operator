package instance

import (
	"crypto/sha1"
	"encoding/base32"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openmcp-project/service-provider-metrics-operator/api/v1alpha1"
)

const (
	labelInstanceID          = "metrics-operator.services.openmcp.cloud/instance-id"
	base32EncodeStdLowerCase = "abcdefghijklmnopqrstuvwxyz234567"
)

// GetID returns the instance id of the MetricsOperator object.
func GetID(o client.Object) string {
	if o.GetLabels() == nil {
		return ""
	}
	return o.GetLabels()[labelInstanceID]
}

// SetID sets the instance id of the MetricsOperator object.
func SetID(o *v1alpha1.MetricsOperator, tenantID string) {
	if o.Labels == nil {
		o.Labels = map[string]string{}
	}
	o.Labels[labelInstanceID] = tenantID
}

// GenerateID generates a new instance id for the given MetricsOperator object.
func GenerateID(o *v1alpha1.MetricsOperator) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "%s/%s", o.Namespace, o.Name)
	id := base32.NewEncoding(base32EncodeStdLowerCase).WithPadding(base32.NoPadding).EncodeToString(h.Sum(nil))
	return id
}

// Namespace prefixes the instance id of the MetricsOperator object to create a tenant namespace.
func Namespace(o *v1alpha1.MetricsOperator) string {
	return fmt.Sprintf("metrics-operator-%s", GetID(o))
}
