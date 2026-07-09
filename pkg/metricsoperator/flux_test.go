package metricsoperator

import (
	"testing"
	"time"

	"github.com/fluxcd/pkg/apis/meta"
	sourcev1 "github.com/fluxcd/source-controller/api/v1"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiv1alpha1 "github.com/openmcp-project/service-provider-metrics-operator/api/v1alpha1"
)

func TestFluxStatus(t *testing.T) {
	now := metav1.Now()

	t.Run("terminating", func(t *testing.T) {
		obj := &sourcev1.OCIRepository{
			ObjectMeta: metav1.ObjectMeta{DeletionTimestamp: &now},
		}
		s := FluxStatus(obj, apiv1alpha1.PlatformCluster)
		assert.Equal(t, apiv1alpha1.Terminating, s.Phase)
	})

	t.Run("ready when ReadyCondition is True", func(t *testing.T) {
		obj := &sourcev1.OCIRepository{}
		obj.Status.Conditions = []metav1.Condition{
			{
				Type:               meta.ReadyCondition,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(time.Now()),
				Reason:             "Succeeded",
			},
		}
		s := FluxStatus(obj, apiv1alpha1.PlatformCluster)
		assert.Equal(t, apiv1alpha1.Ready, s.Phase)
	})

	t.Run("pending when not ready", func(t *testing.T) {
		obj := &sourcev1.OCIRepository{}
		s := FluxStatus(obj, apiv1alpha1.PlatformCluster)
		assert.Equal(t, apiv1alpha1.Pending, s.Phase)
	})
}
