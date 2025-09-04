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
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	learningv1alpha1 "aca.com/bg-deployment-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	bgd := learningv1alpha1.BlueGreenDeployment{}

	err := r.Get(ctx, req.NamespacedName, &bgd)

	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}

	if err != nil {
		log.Error(err, "unable to fetch BlueGreenDeployment")
		return ctrl.Result{}, err
	}

	tests := bgd.Spec.Tests
	if len(tests) == 0 {
		log.Info("No tests defined, skipping")
		bgd.Status.Phase = learningv1alpha1.PhaseSucceeded
		err := r.Status().Update(ctx, &bgd)
		if err != nil {
			log.Error(err, "unable to update BlueGreenDeployment status")
			return ctrl.Result{}, err
		}
	}

	for _, test := range tests {
		creationTime := bgd.ObjectMeta.CreationTimestamp.Time
		initialDelay := time.Duration(test.Http.InitialDelaySeconds) * time.Second
		now := time.Now()

		if now.Before(creationTime.Add(initialDelay)) {
			requeueAfter := creationTime.Add(initialDelay).Sub(now)
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}

		log.Info("Running test", "name", test.Name)
		passed, err := r.runHTTPTest(&bgd, &test)
		if err != nil {
			log.Error(err, "error running test", "name", test.Name)
			bgd.Status.Phase = learningv1alpha1.PhaseFailed
			err := r.Status().Update(ctx, &bgd)
			if err != nil {
				log.Error(err, "unable to update BlueGreenDeployment status")
				addStatusCondition(
					&bgd,
					learningv1alpha1.ConditionRunTests,
					metav1.ConditionFalse,
					learningv1alpha1.ReasonTestsFailed,
					err.Error(),
				)
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, err
		}

		if !passed {
			log.Info("Test failed", "name", test.Name)
			bgd.Status.Phase = learningv1alpha1.PhaseFailed
			err := r.Status().Update(ctx, &bgd)
			if err != nil {
				log.Error(err, "unable to update BlueGreenDeployment status")
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}

		log.Info("Test passed", "name", test.Name)
		addStatusCondition(
			&bgd,
			learningv1alpha1.ConditionRunTests,
			metav1.ConditionTrue,
			learningv1alpha1.ReasonTestsPassed,
			fmt.Sprintf("Test %s passed", test.Name),
		)
		err = r.Status().Update(ctx, &bgd)
		if err != nil {
			log.Error(err, "unable to update BlueGreenDeployment status")
			return ctrl.Result{}, err
		}
	}

	// all tests passed
	log.Info("All tests passed")
	bgd.Status.Phase = learningv1alpha1.PhasePromoting
	err = r.Status().Update(ctx, &bgd)
	if err != nil {
		log.Error(err, "unable to update BlueGreenDeployment status")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *BlueGreenDeploymentTesterReconciler) runHTTPTest(bgd *learningv1alpha1.BlueGreenDeployment, test *learningv1alpha1.TestSpec) (bool, error) {
	s := corev1.Service{}
	err := r.Get(context.Background(), client.ObjectKey{Namespace: bgd.Namespace, Name: "green-" + bgd.Name}, &s)
	if err != nil {
		return false, fmt.Errorf("error getting green service: %w", err)
	}

	client := &http.Client{
		Timeout: time.Duration(test.Http.TimeoutSeconds) * time.Second,
	}

	url := fmt.Sprintf("http://%s.%s.svc:%d%s",
		"green-"+bgd.Name,
		s.Namespace,
		test.Http.Port,
		test.Http.Path,
	)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return false, fmt.Errorf("error creating request: %w", err)
	}

	for k, v := range test.Http.HttpHeaders {
		req.Header.Add(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("error making request: %w", err)
	}
	defer resp.Body.Close()

	isValidRespCode := slices.Contains(test.Http.ExpectedStatusCodes, int32(resp.StatusCode))
	if !isValidRespCode {
		return false, errors.New("test failed with unexpected status code: " + fmt.Sprintf("%d", resp.StatusCode))
	}

	return true, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *BlueGreenDeploymentTesterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&learningv1alpha1.BlueGreenDeployment{}).
		WithEventFilter(predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				switch e.ObjectNew.(type) {
				case *learningv1alpha1.BlueGreenDeployment:
					oldBGD := e.ObjectOld.(*learningv1alpha1.BlueGreenDeployment)
					newBGD := e.ObjectNew.(*learningv1alpha1.BlueGreenDeployment)
					// Only reconcile if Phase changed
					return oldBGD.Status.Phase != newBGD.Status.Phase &&
						newBGD.Status.Phase == learningv1alpha1.PhaseRunTests
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
