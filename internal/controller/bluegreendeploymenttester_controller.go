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

package controller

import (
	"context"

	learningv1alpha1 "aca.com/bg-deployment-controller/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// BlueGreenDeploymentTesterReconciler reconciles a BlueGreenDeploymentTester object
type BlueGreenDeploymentTesterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=learning.aca.com,resources=bluegreendeploymenttesters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=learning.aca.com,resources=bluegreendeploymenttesters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=learning.aca.com,resources=bluegreendeploymenttesters/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *BlueGreenDeploymentTesterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	log.Info("RunTests for BlueGreenDeployment")

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BlueGreenDeploymentTesterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&learningv1alpha1.BlueGreenDeployment{}).
		WithEventFilter(predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				switch e.ObjectNew.(type) {
				case *learningv1alpha1.BlueGreenDeployment:
					newObj := e.ObjectNew.(*learningv1alpha1.BlueGreenDeployment)
					return newObj.Status.Phase == learningv1alpha1.PhaseRunTests
				default:
					return false
				}
			},
			CreateFunc: func(e event.CreateEvent) bool {
				switch e.Object.(type) {
				case *learningv1alpha1.BlueGreenDeployment:
					newObj := e.Object.(*learningv1alpha1.BlueGreenDeployment)
					return newObj.Status.Phase == learningv1alpha1.PhaseRunTests
				default:
					return false
				}
			},
			DeleteFunc: func(e event.DeleteEvent) bool { return false },
		}).
		Named("bluegreendeploymenttester").
		Complete(r)
}
