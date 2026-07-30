package mcpresources

import (
	"context"
	"fmt"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	metricsGroup   = "metrics.openmcp.cloud"
	metricsVersion = "v1alpha1"
)

// metricsListGVKs are the list GVKs for CRs that the metrics-operator installs on the MCP.
var metricsListGVKs = []schema.GroupVersionKind{
	{Group: metricsGroup, Version: metricsVersion, Kind: "MetricList"},
	{Group: metricsGroup, Version: metricsVersion, Kind: "ManagedMetricList"},
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
			blocking = append(blocking, gvk.Kind[:len(gvk.Kind)-4])
		}
	}
	return blocking, nil
}
