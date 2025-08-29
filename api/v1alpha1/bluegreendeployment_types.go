/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// BlueGreenDeploymentSpec defines the desired state of BlueGreenDeployment
type BlueGreenDeploymentSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// The following markers will use OpenAPI v3 schema to validate the value
	// More info: https://book.kubebuilder.io/reference/markers/crd-validation.html

	// +kubebuilder:validation:Required
	Deployment DeploymentSpec `json:"deployment"`

	// +kubebuilder:validation:Required
	Service ServiceSpec `json:"service"`
}

type DeploymentSpec struct {
	appsv1.DeploymentSpec `json:",inline"`
}

type ServiceSpec struct {
	corev1.ServiceSpec `json:",inline"`
}

// BlueGreenDeploymentStatus defines the observed state of BlueGreenDeployment.
type BlueGreenDeploymentStatus struct {
	Phase      BlueGreenPhase     `json:"phase,omitempty"`
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type" protobuf:"bytes,1,rep,name=conditions"`
}

type BlueGreenPhase string

const (
	// PhasePending means the CR has been created but no deployments/services exist yet.
	PhasePending BlueGreenPhase = "Pending"

	// PhaseDeploying means the active deployment/service are being created or updated.
	// The controller waits until the deployment pods are ready.
	PhaseDeploying BlueGreenPhase = "Deploying"

	// PhaseRunTests means the new deployment is ready and tests should be executed.
	PhaseRunTests BlueGreenPhase = "RunTests"

	// PhasePromoting means the service is being switched to point to the new deployment.
	PhasePromoting BlueGreenPhase = "Promoting"

	// PhaseCleaningUp means old/inactive deployments and services are being deleted.
	PhaseCleaningUp BlueGreenPhase = "CleaningUp"

	// PhaseSucceeded means rollout is complete and system is stable.
	PhaseSucceeded BlueGreenPhase = "Succeeded"

	// PhaseFailed means tests failed. The new deployment remains inactive for debugging.
	PhaseFailed BlueGreenPhase = "Failed"
)

type BlueGreenConditionType string

const (
	ConditionReady      BlueGreenConditionType = "Ready"
	ConditionDeploying  BlueGreenConditionType = "Deploying"
	ConditionRunTests   BlueGreenConditionType = "RunTests"
	ConditionPromoting  BlueGreenConditionType = "Promoting"
	ConditionCleaningUp BlueGreenConditionType = "CleaningUp"
	ConditionSucceeded  BlueGreenConditionType = "Succeeded"
	ConditionFailed     BlueGreenConditionType = "Failed"
)

type BlueGreenConditionReason string

const (
	ReasonDeploymentError     BlueGreenConditionReason = "Error"
	ReasonDeploymentCreated   BlueGreenConditionReason = "DeploymentCreated"
	ReasonDeploymentUpdated   BlueGreenConditionReason = "DeploymentUpdated"
	ReasonServiceCreated      BlueGreenConditionReason = "ServiceCreated"
	ReasonServiceUpdated      BlueGreenConditionReason = "ServiceUpdated"
	ReasonDeploymentReady     BlueGreenConditionReason = "DeploymentReady"
	ReasonTestsPassed         BlueGreenConditionReason = "TestsPassed"
	ReasonTestsFailed         BlueGreenConditionReason = "TestsFailed"
	ReasonServicePromoted     BlueGreenConditionReason = "ServicePromoted"
	ReasonOldResourcesDeleted BlueGreenConditionReason = "OldResourcesDeleted"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// BlueGreenDeployment is the Schema for the bluegreendeployments API
type BlueGreenDeployment struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec defines the desired state of BlueGreenDeployment
	// +required
	Spec BlueGreenDeploymentSpec `json:"spec"`

	// status defines the observed state of BlueGreenDeployment
	// +optional
	Status BlueGreenDeploymentStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// BlueGreenDeploymentList contains a list of BlueGreenDeployment
type BlueGreenDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []BlueGreenDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&BlueGreenDeployment{}, &BlueGreenDeploymentList{})
}
