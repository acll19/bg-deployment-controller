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
	"strconv"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	learningv1alpha1 "aca.com/bg-deployment-controller/api/v1alpha1"
)

// BlueGreenDeploymentReconciler reconciles a BlueGreenDeployment object
type BlueGreenDeploymentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=learning.aca.com,resources=bluegreendeployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=learning.aca.com,resources=bluegreendeployments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=learning.aca.com,resources=bluegreendeployments/finalizers,verbs=update

// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the BlueGreenDeployment object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.21.0/pkg/reconcile
func (r *BlueGreenDeploymentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	log.Info("Reconciliation triggered")
	bgd := learningv1alpha1.BlueGreenDeployment{}

	err := r.Get(ctx, req.NamespacedName, &bgd)

	if errors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}

	if err != nil {
		return ctrl.Result{}, err
	}

	switch bgd.Status.Phase {
	case "",
		learningv1alpha1.PhasePending:
		// create active deploy and svc
		// set phase to Pending
		// return
		log.Info("creating deployment")
		active := false
		color := "blue"
		d := r.deploymentForBGD(&bgd, color, active)
		err = r.Create(ctx, &d)
		if err != nil {
			log.Error(err, "unable to create deployment")
			return ctrl.Result{}, err
		}

		log.Info("creating service")
		s := r.serviceForBGD(&bgd, color, active)
		err = r.Create(ctx, &s)
		if err != nil {
			log.Error(err, "unable to create service")
			return ctrl.Result{}, err
		}
		bgd.Status.Phase = learningv1alpha1.PhaseDeploying
		// TODO: update conditions
	case learningv1alpha1.PhaseDeploying:
		// check status of the deploy and svc
		// if ready, change phase to Deploying
		// return
		active := false
		deploys, err := r.listDeploymentsForBGD(ctx, &bgd, active)
		if err != nil {
			log.Error(err, "unable to fetch blue deployment for BGD")
			return ctrl.Result{}, err
		}

		if len(deploys.Items) == 0 {
			// TODO: recreate the deployment. This should not happen.
			log.Error(err, "no deployments found for BGD")
			return ctrl.Result{}, nil
		}

		services, err := r.listServicesForBGD(ctx, &bgd, "blue")

		if len(services.Items) == 0 {
			// TODO: recreate the service. This should not happen.
			log.Error(err, "no services found for BGD")
			return ctrl.Result{}, nil
		}

		deploy := deploys.Items[0]
		service := services.Items[0]
		// Check service readiness based on type
		serviceReady := false
		switch service.Spec.Type {
		case corev1.ServiceTypeLoadBalancer:
			serviceReady = r.checkLoadBalancerServiceTypeStatus(service)
		case corev1.ServiceTypeClusterIP:
			serviceReady = r.checkClusterIpServiceTypeStatus(service)
		case corev1.ServiceTypeNodePort:
			serviceReady = r.checkNodePortServiceTypeStatus(service)
		case corev1.ServiceTypeExternalName:
			serviceReady = r.checkExternalNameServiceTypeStatus(service)
		}

		// Check if we trigger promotion
		if serviceReady && deploy.Status.ReadyReplicas == *deploy.Spec.Replicas {
			bgd.Status.Phase = learningv1alpha1.PhaseRunTests
		}
		// TODO: update conditions

	case learningv1alpha1.PhasePromoting: // after tests are done
		// update live svc selector to point to new deploy
		// update new deploy label to active (or live?)
		// update former active deploy label to inactive
		// if promotion has gone will, change phase to cleanup
		// check if there is an active deployment already
		active := true
		activeDeploys, err := r.fetchDeployments(ctx, bgd, active)
		if err != nil {
			log.Error(err, "unable to fetch active deployment for BGD")
			return ctrl.Result{}, err
		}

		var activeD *appsv1.Deployment
		if activeDeploys.Size() > 0 {
			activeD = &activeDeploys.Items[0]
			activeD.Labels["cleanup"] = "true" // mark for cleanup
		}

		active = false
		inactiveDeploys, err := r.listDeploymentsForBGD(ctx, &bgd, active)
		if err != nil {
			log.Error(err, "unable to fetch blue deployment for BGD")
			return ctrl.Result{}, err
		}

		var deployToPromote *appsv1.Deployment
		for _, d := range inactiveDeploys.Items {
			if d.Labels["cleanup"] != "true" {
				deployToPromote = &d
				break
			}
		}

		if deployToPromote == nil {
			// TODO: recreate the deployment. This should not happen.
			err := errors.NewNotFound(appsv1.Resource("deployments"), "no matching deployment found to promote")
			log.Error(err, "unable to find deployment to promote for BGD")
			return ctrl.Result{}, err
		}

		deployToPromote.Labels["active"] = "true"
		deployToPromote.Labels["color"] = "green"

		// Execute all updates in a transaction-like manner
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			if activeD != nil {
				if err := r.Update(ctx, activeD); err != nil {
					return err
				}
			}
			if deployToPromote != nil {
				if err := r.Update(ctx, deployToPromote); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			log.Error(err, "failed to update deployments during promotion")
			return ctrl.Result{}, err
		}

		bgd.Status.Phase = learningv1alpha1.PhaseCleaningUp
		// TODO: update conditions
	}

	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		err = r.Get(ctx, req.NamespacedName, &bgd)
		if err != nil {
			log.Error(err, "unable to fetch BGD for status update")
			return err
		}

		err = r.Status().Update(ctx, &bgd)
		if err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		log.Error(err, "unable to update BGD status after retries")
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *BlueGreenDeploymentReconciler) fetchDeployments(ctx context.Context, bgd learningv1alpha1.BlueGreenDeployment, active bool) (appsv1.DeploymentList, error) {
	activeDeploys := appsv1.DeploymentList{}
	activeDeploysSelector := client.MatchingLabels{"app": bgd.Name, "active": strconv.FormatBool(active)}
	err := r.List(ctx, &activeDeploys, activeDeploysSelector)
	return activeDeploys, err
}

func (r *BlueGreenDeploymentReconciler) listDeploymentsForBGD(ctx context.Context, bgd *learningv1alpha1.BlueGreenDeployment, active bool) (appsv1.DeploymentList, error) {
	deploys := appsv1.DeploymentList{}
	labelSelector := client.MatchingLabels{"app": bgd.Name, "active": strconv.FormatBool(active)}
	err := r.List(ctx, &deploys, labelSelector)
	if err != nil {
		return deploys, err
	}
	return deploys, nil
}

func (r *BlueGreenDeploymentReconciler) listServicesForBGD(ctx context.Context, bgd *learningv1alpha1.BlueGreenDeployment, color string) (corev1.ServiceList, error) {
	services := corev1.ServiceList{}
	labelSelector := client.MatchingLabels{"app": bgd.Name, "color": color}
	err := r.List(ctx, &services, labelSelector)
	if err != nil {
		return services, err
	}
	return services, nil
}

// For LoadBalancer, we need an ingress IP/hostname
func (*BlueGreenDeploymentReconciler) checkLoadBalancerServiceTypeStatus(service corev1.Service) bool {
	for _, ingress := range service.Status.LoadBalancer.Ingress {
		if ingress.IP != "" || ingress.Hostname != "" {
			return true
		}
	}
	return false
}

// For ClusterIP, having an IP is enough
func (*BlueGreenDeploymentReconciler) checkClusterIpServiceTypeStatus(service corev1.Service) bool {
	return service.Spec.ClusterIP != ""
}

// For NodePort, having cluster IP and assigned node ports
func (*BlueGreenDeploymentReconciler) checkNodePortServiceTypeStatus(service corev1.Service) bool {
	return service.Spec.ClusterIP != "" && len(service.Spec.Ports) > 0 && service.Spec.Ports[0].NodePort != 0
}

// For ExternalName, just need the external name set
func (*BlueGreenDeploymentReconciler) checkExternalNameServiceTypeStatus(service corev1.Service) bool {
	return service.Spec.ExternalName != ""
}

func (r *BlueGreenDeploymentReconciler) deploymentForBGD(bgd *learningv1alpha1.BlueGreenDeployment, color string, active bool) appsv1.Deployment {
	d := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: bgd.Name + "-", // K8s will append a random suffix
			Namespace:    bgd.Namespace,
			Labels: map[string]string{
				"app":    bgd.Name,
				"color":  color,
				"active": strconv.FormatBool(active),
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(bgd, bgd.GroupVersionKind()),
			},
		},
		Spec: *bgd.Spec.Deployment.DeploymentSpec.DeepCopy(),
	}
	return d
}

func (r *BlueGreenDeploymentReconciler) serviceForBGD(bgd *learningv1alpha1.BlueGreenDeployment, color string, active bool) corev1.Service {
	svcSpec := bgd.Spec.Service.ServiceSpec
	namePrefix := "active-"
	if !active {
		namePrefix = "blue-"
	}
	s := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      namePrefix + bgd.Name,
			Namespace: bgd.Namespace,
			Labels: map[string]string{
				"app":   bgd.Name,
				"color": color,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(bgd, bgd.GroupVersionKind()),
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{
				"app":    bgd.Name,
				"active": strconv.FormatBool(active),
				"color":  color,
			},
			Type:            svcSpec.Type,
			Ports:           svcSpec.Ports,
			SessionAffinity: svcSpec.SessionAffinity,
		},
	}
	return s
}

// SetupWithManager sets up the controller with the Manager.
func (r *BlueGreenDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&learningv1alpha1.BlueGreenDeployment{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		WithEventFilter(predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				newObj := e.ObjectNew.(*learningv1alpha1.BlueGreenDeployment)
				oldObj := e.ObjectOld.(*learningv1alpha1.BlueGreenDeployment)
				// Only enqueue reconcile if status has changed
				if newObj.Status.Phase != oldObj.Status.Phase {
					switch newObj.Status.Phase {
					case "",
						learningv1alpha1.PhasePending,
						learningv1alpha1.PhaseDeploying,
						learningv1alpha1.PhasePromoting,
						learningv1alpha1.PhaseSucceeded,
						learningv1alpha1.PhaseFailed:
						return true
					default:
						return false
					}
				}

				return false
			},
			CreateFunc: func(e event.CreateEvent) bool {
				return true // always run on new CRs
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				return false
			},
		}).
		Named("bluegreendeployment").
		Complete(r)
}
