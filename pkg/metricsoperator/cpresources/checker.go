package cpresources

import (
	"context"
	"fmt"
	"strings"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	MetricsGroup   = "metrics.open-control-plane.io"
	MetricsVersion = "v1alpha1"
)

// metricsListGVKs are the list GVKs for CRs that the metrics-operator installs on the CP.
// Keep in sync with https://github.com/openmcp-project/metrics-operator/tree/main/api/v1alpha1
var metricsListGVKs = []schema.GroupVersionKind{
	{Group: MetricsGroup, Version: MetricsVersion, Kind: "DataSinkList"},
	{Group: MetricsGroup, Version: MetricsVersion, Kind: "FederatedClusterAccessList"},
	{Group: MetricsGroup, Version: MetricsVersion, Kind: "FederatedManagedMetricList"},
	{Group: MetricsGroup, Version: MetricsVersion, Kind: "FederatedMetricList"},
	{Group: MetricsGroup, Version: MetricsVersion, Kind: "ManagedMetricList"},
	{Group: MetricsGroup, Version: MetricsVersion, Kind: "MetricList"},
	{Group: MetricsGroup, Version: MetricsVersion, Kind: "RemoteClusterAccessList"},
}

// BlockingKinds lists the CRD kinds that still have instances on the cluster, blocking deletion.
// Returns an empty slice when none exist or the CRDs are not installed.
func BlockingKinds(ctx context.Context, cl client.Client) ([]string, error) {
	var blocking []string
	for _, gvk := range metricsListGVKs {
		list := &unstructured.UnstructuredList{}
		list.SetGroupVersionKind(gvk)
		if err := cl.List(ctx, list); err != nil {
			if apimeta.IsNoMatchError(err) {
				continue
			}
			return nil, fmt.Errorf("listing %s: %w", gvk.Kind, err)
		}
		if len(list.Items) > 0 {
			// Report the singular Kind (strip "List" suffix) for the condition message.
			blocking = append(blocking, strings.TrimSuffix(gvk.Kind, "List"))
		}
	}
	return blocking, nil
}
