package controller

import (
	"context"

	"github.com/openshift-hyperfleet/hyperfleet-adapter/pkg/desire"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const fieldManager = "hyperfleet-applier"

// ApplyOptions returns metav1.ApplyOptions for server-side apply with force.
func ApplyOptions() metav1.ApplyOptions {
	return metav1.ApplyOptions{
		FieldManager: fieldManager,
		Force:        true,
	}
}

// GetOptions returns metav1.GetOptions.
func GetOptions() metav1.GetOptions {
	return metav1.GetOptions{}
}

// DeleteOptions returns metav1.DeleteOptions.
func DeleteOptions() metav1.DeleteOptions {
	return metav1.DeleteOptions{}
}

// isNotFound checks if an error is a Kubernetes NotFound error.
func isNotFound(err error) bool {
	return apierrors.IsNotFound(err)
}

// makeCondition builds a metav1.Condition for a desire status.
func makeCondition(condType string, success bool, reason, message string) metav1.Condition {
	status := metav1.ConditionTrue
	if !success {
		status = metav1.ConditionFalse
	}
	return metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	}
}

// writeApplyStatus writes the Successful condition back to the store for an ApplyDesire.
func (r *Reconciler) writeApplyStatus(ctx context.Context, d *desire.ApplyDesire, success bool, reason, message string) {
	status := desire.DesireStatus{
		Conditions: []metav1.Condition{
			makeCondition(desire.ConditionTypeSuccessful, success, reason, message),
		},
	}
	if err := r.store.UpdateApplyDesireStatus(ctx, d.ID, status, d.Version); err != nil {
		r.logger.Error("failed to write apply desire status",
			"desire", d.ID.Name, "error", err)
	}
}

// writeDeleteStatus writes the Successful condition back to the store for a DeleteDesire.
func (r *Reconciler) writeDeleteStatus(ctx context.Context, d *desire.DeleteDesire, success bool, reason, message string) {
	status := desire.DesireStatus{
		Conditions: []metav1.Condition{
			makeCondition(desire.ConditionTypeSuccessful, success, reason, message),
		},
	}
	if err := r.store.UpdateDeleteDesireStatus(ctx, d.ID, status, d.Version); err != nil {
		r.logger.Error("failed to write delete desire status",
			"desire", d.ID.Name, "error", err)
	}
}

// writeReadStatus writes the status back to the store for a ReadDesire.
func (r *Reconciler) writeReadStatus(ctx context.Context, d *desire.ReadDesire, success bool, reason, message string, kubeContent []byte) {
	status := desire.ReadDesireStatus{
		DesireStatus: desire.DesireStatus{
			Conditions: []metav1.Condition{
				makeCondition(desire.ConditionTypeSuccessful, success, reason, message),
			},
		},
		KubeContent: kubeContent,
	}
	if err := r.store.UpdateReadDesireStatus(ctx, d.ID, status, d.Version); err != nil {
		r.logger.Error("failed to write read desire status",
			"desire", d.ID.Name, "error", err)
	}
}
