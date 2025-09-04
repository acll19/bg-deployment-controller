package controller

import (
	"encoding/json"
	"fmt"
	"hash/fnv"

	learningv1alpha1 "aca.com/bg-deployment-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func hashObj(obj any) (string, error) {
	hash := fnv.New32a()
	err := json.NewEncoder(hash).Encode(obj)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hash.Sum32()), nil
}

func addStatusCondition(bgd *learningv1alpha1.BlueGreenDeployment, conditionType learningv1alpha1.BlueGreenConditionType, status metav1.ConditionStatus, reason learningv1alpha1.BlueGreenConditionReason, message string) {
	now := metav1.Now()
	condition := metav1.Condition{
		Type:               string(conditionType),
		Status:             status,
		LastTransitionTime: now,
		Reason:             string(reason),
		Message:            message,
	}

	meta.SetStatusCondition(&bgd.Status.Conditions, condition)
}
