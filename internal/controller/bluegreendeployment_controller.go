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
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
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
		log.Error(err, "unable to fetch BlueGreenDeployment")
		return ctrl.Result{}, err
	}

	blueSvc, err := r.listServicesForBGD(ctx, "blue")
	if err != nil {
		log.Error(err, "unable to list blue services for BGD")
		return ctrl.Result{}, err
	}

	// Reconcile blue service
	if len(blueSvc.Items) == 0 && bgd.Status.Phase != learningv1alpha1.PhasePending && bgd.Status.Phase != "" {
		// Service does not exist, create it
		log.Info("Blue service does not exist, creating")
		s := r.serviceForBGD(&bgd, "blue", false)
		err = r.Create(ctx, &s)
		if err != nil {
			log.Error(err, "unable to create blue service")
			return ctrl.Result{}, err
		}
	} else if bgd.Status.Phase != learningv1alpha1.PhasePending && bgd.Status.Phase != "" {
		svc := blueSvc.Items[0]
		if equality.Semantic.DeepEqual(svc.Spec.Ports, bgd.Spec.Service.Ports) ||
			equality.Semantic.DeepEqual(svc.Spec.Selector, bgd.Spec.Service.Selector) ||
			svc.Spec.Type != bgd.Spec.Service.Type {
			log.Info("Blue service type has changed, updating")
			svc.Spec.Selector = bgd.Spec.Service.Selector
			svc.Spec.Ports = bgd.Spec.Service.Ports
			svc.Spec.Type = bgd.Spec.Service.Type
			err = r.Update(ctx, &svc)
			if err != nil {
				log.Error(err, "unable to update blue service")
				return ctrl.Result{}, err
			}
			log.Info("Blue service updated successfully")
		}
	}
	// End reconcile blue service

	switch bgd.Status.Phase {
	case "",
		learningv1alpha1.PhasePending:
		log.Info("creating preview deployment")
		active := false
		color := "blue"
		d := r.deploymentForBGD(&bgd, color, active)
		err = r.Create(ctx, &d)
		if err != nil {
			log.Error(err, "unable to create deployment")
			return ctrl.Result{}, err
		}

		active = true
		color = "green"
		deploys, err := r.listDeploymentsForBGD(ctx, active)
		if err != nil {
			log.Error(err, "unable to fetch active deployment for BGD")
			return ctrl.Result{}, err
		}
		if len(deploys.Items) == 0 {
			log.Info("creating active deployment")
			d = r.deploymentForBGD(&bgd, color, active)
			replicas := int32(0)
			d.Spec.Replicas = &replicas
			err = r.Create(ctx, &d)
			if err != nil {
				log.Error(err, "unable to create deployment")
				return ctrl.Result{}, err
			}
		}

		log.Info("creating blue service")
		active = false
		color = "blue"
		s := r.serviceForBGD(&bgd, color, active)
		err = r.Create(ctx, &s)
		if err != nil {
			log.Error(err, "unable to create service")
			return ctrl.Result{}, err
		}
		bgd.Status.Phase = learningv1alpha1.PhaseDeploying
		r.addStatusCondition(
			&bgd,
			learningv1alpha1.ConditionDeploying,
			metav1.ConditionTrue,
			learningv1alpha1.ReasonDeploymentCreated,
			"Deployment and Service created, waiting for pods to be ready",
		)
	case learningv1alpha1.PhaseDeploying:
		active := false
		deploys, err := r.listDeploymentsForBGD(ctx, active)
		if err != nil {
			log.Error(err, "unable to fetch blue deployment for BGD")
			return ctrl.Result{}, err
		}

		services, err := r.listServicesForBGD(ctx, "blue")
		if err != nil {
			log.Error(err, "unable to fetch blue service for BGD")
			return ctrl.Result{}, err
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
			r.addStatusCondition(
				&bgd,
				learningv1alpha1.ConditionRunTests,
				metav1.ConditionTrue,
				learningv1alpha1.ReasonDeploymentCreated,
				"Deployment and Service are ready, proceed with tests",
			)
		}

	case learningv1alpha1.PhasePromoting: // this phase is set by the tester controller when tests pass
		// create active service if it doesn't exist (could happen during the first ever rollout)
		activeServices, err := r.listServicesForBGD(ctx, "active")
		if errors.IsNotFound(err) || len(activeServices.Items) == 0 {
			log.Info("Active service does not exist, creating")
			s := r.serviceForBGD(&bgd, "active", true)
			err = r.Create(ctx, &s)
			if err != nil {
				log.Error(err, "unable to create active service")
				return ctrl.Result{}, err
			}
		} else if err != nil {
			log.Error(err, "unable to fetch active service for BGD")
			return ctrl.Result{}, err
		}

		active := false
		inactiveDeploys, err := r.listDeploymentsForBGD(ctx, active)
		if err != nil {
			log.Error(err, "unable to fetch blue deployment for BGD")
			return ctrl.Result{}, err
		}

		preview := inactiveDeploys.Items[0]

		active = true
		activeDeploys, err := r.listDeploymentsForBGD(ctx, active)
		if err != nil {
			log.Error(err, "unable to fetch active deployment for BGD")
			return ctrl.Result{}, err
		}
		activeDeploy := activeDeploys.Items[0]

		// Here's the actual promotion, just swapping spec.template
		activeDeploy.Spec.Template.Spec = preview.Spec.Template.Spec
		activeDeploy.Spec.Replicas = bgd.Spec.Deployment.Replicas
		// Then set preview replicas to 0
		replicas := int32(0)
		preview.Spec.Replicas = &replicas

		if result, err := r.retryUpdateOnConflict(ctx, "update deployments during promotion", func() error {
			if err := r.Update(ctx, &activeDeploy); err != nil {
				return err
			}

			if err := r.Update(ctx, &preview); err != nil {
				return err
			}
			return nil
		}); err != nil {
			return result, err
		}

		bgd.Status.Phase = learningv1alpha1.PhaseCleaningUp
		r.addStatusCondition(
			&bgd,
			learningv1alpha1.ConditionPromoting,
			metav1.ConditionTrue,
			learningv1alpha1.ReasonServiceUpdated,
			"Service updated to point to new deployment, cleaning up old resources",
		)
	}

	result, err := r.retryUpdateOnConflict(ctx, "update BGD status", func() error {
		status := bgd.Status.DeepCopy()
		if err := r.Get(ctx, req.NamespacedName, &bgd); err != nil {
			return err
		}
		bgd.Status = *status
		return r.Status().Update(ctx, &bgd)
	})

	if err != nil {
		// TODO: handle status error
		return result, err
	}

	return ctrl.Result{}, nil
}

func (r *BlueGreenDeploymentReconciler) listDeploymentsForBGD(ctx context.Context, active bool) (appsv1.DeploymentList, error) {
	deploys := appsv1.DeploymentList{}
	labelSelector := client.MatchingLabels{
		"active": strconv.FormatBool(active),
	}
	if err := r.List(ctx, &deploys, labelSelector); err != nil {
		return deploys, err
	}
	return deploys, nil
}

func (r *BlueGreenDeploymentReconciler) listServicesForBGD(ctx context.Context, color string) (corev1.ServiceList, error) {
	services := corev1.ServiceList{}
	labelSelector := client.MatchingLabels{"color": color}
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

func (*BlueGreenDeploymentReconciler) deploymentForBGD(bgd *learningv1alpha1.BlueGreenDeployment, color string, active bool) appsv1.Deployment {
	spec := *bgd.Spec.Deployment.DeepCopy()
	selector := spec.Selector
	if selector.MatchLabels == nil {
		selector.MatchLabels = make(map[string]string)
	}
	selector.MatchLabels["active"] = strconv.FormatBool(active)
	selector.MatchLabels["color"] = color
	spec.Selector = selector

	templateLabels := spec.Template.ObjectMeta.Labels
	templateLabels["active"] = strconv.FormatBool(active)
	templateLabels["color"] = color
	spec.Template.ObjectMeta.Labels = templateLabels

	bgd.Spec.Deployment = spec

	d := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: color + bgd.Name + "-", // K8s will append a random suffix
			Namespace:    bgd.Namespace,
			Labels: map[string]string{
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

func (*BlueGreenDeploymentReconciler) serviceForBGD(bgd *learningv1alpha1.BlueGreenDeployment, color string, active bool) corev1.Service {
	svcSpec := bgd.Spec.Service
	namePrefix := "active-"
	if !active {
		namePrefix = "blue-"
	}

	selector := bgd.Spec.Service.Selector
	if selector == nil {
		selector = make(map[string]string)
	}
	selector["active"] = strconv.FormatBool(active)
	selector["color"] = color

	s := corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      namePrefix + bgd.Name,
			Namespace: bgd.Namespace,
			Labels: map[string]string{
				"color": color,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(bgd, bgd.GroupVersionKind()),
			},
		},
		Spec: corev1.ServiceSpec{
			Selector: selector,
			Type:     svcSpec.Type,
			Ports:    svcSpec.Ports,
		},
	}
	return s
}

// retryUpdateOnConflict executes the provided update function with retries on conflict
func (*BlueGreenDeploymentReconciler) retryUpdateOnConflict(ctx context.Context, failureMsg string, updateFn func() error) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	if err := retry.RetryOnConflict(retry.DefaultRetry, updateFn); err != nil {
		log.Error(err, "failed to "+failureMsg)
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

func (*BlueGreenDeploymentReconciler) addStatusCondition(bgd *learningv1alpha1.BlueGreenDeployment, conditionType learningv1alpha1.BlueGreenConditionType, status metav1.ConditionStatus, reason learningv1alpha1.BlueGreenConditionReason, message string) {
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

// SetupWithManager sets up the controller with the Manager.
func (r *BlueGreenDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&learningv1alpha1.BlueGreenDeployment{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		WithEventFilter(predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				switch e.ObjectNew.(type) {
				case *appsv1.Deployment, *corev1.Service:
					return true
				default:
					newBGDObj := e.ObjectNew.(*learningv1alpha1.BlueGreenDeployment)
					oldBGDObj := e.ObjectOld.(*learningv1alpha1.BlueGreenDeployment)
					// Only enqueue reconcile if status has changed
					if newBGDObj.Status.Phase != oldBGDObj.Status.Phase {
						switch newBGDObj.Status.Phase {
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
