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
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}

	if err != nil {
		log.Error(err, "unable to fetch BlueGreenDeployment")
		return ctrl.Result{}, err
	}

	blueReplicaSets := appsv1.ReplicaSetList{}
	blueReplicaSets, err = r.listReplicaSetsForBGD(ctx, false)
	if err != nil {
		log.Error(err, "unable to list green deployments for BGD")
		return ctrl.Result{}, err
	}

	if len(blueReplicaSets.Items) > 0 {
		rs := blueReplicaSets.Items[0]
		specHash, err := hashObj(bgd.Spec.Deployment.DeploymentSpec.Template)
		if err != nil {
			log.Error(err, "unable to hash replicaset spec")
			return ctrl.Result{}, err
		}

		if rs.Annotations["specHash"] == "" {
			log.Info("Adding specHash label to existing replicaset")
			if rs.Annotations == nil {
				rs.Annotations = make(map[string]string)
			}
			rs.Annotations["specHash"] = specHash
			err = r.Update(ctx, &rs)
			if err != nil {
				log.Error(err, "unable to update replicaset with specHash label")
				return ctrl.Result{}, err
			}
		} else if rs.Annotations["specHash"] != specHash {
			log.Info("Deployment spec has changed, moving to Pending phase")
			bgd.Status.Phase = learningv1alpha1.PhasePending
			rs.Annotations["specHash"] = specHash
			if err = r.Update(ctx, &rs); err != nil {
				log.Error(err, "unable to update replicaset with new specHash label")
				return ctrl.Result{}, err
			}
		}
	}

	blueSvcs, err := r.listServicesForBGD(ctx, "green")
	if err != nil {
		log.Error(err, "unable to list green services for BGD")
		return ctrl.Result{}, err
	}

	if len(blueSvcs.Items) > 0 {
		svc := blueSvcs.Items[0]
		specHash, err := hashObj(bgd.Spec.Service.ServiceSpec)
		if err != nil {
			log.Error(err, "unable to hash service spec")
			return ctrl.Result{}, err
		}

		if svc.Annotations["specHash"] == "" {
			log.Info("Adding specHash label to existing service")
			if svc.Annotations == nil {
				svc.Annotations = make(map[string]string)
			}
			svc.Annotations["specHash"] = specHash
			err = r.Update(ctx, &svc)
			if err != nil {
				log.Error(err, "unable to update service with specHash label")
				return ctrl.Result{}, err
			}
		} else if svc.Annotations["specHash"] != specHash {
			log.Info("Service spec has changed, moving to Pending phase")
			bgd.Status.Phase = learningv1alpha1.PhasePending
			svc.Annotations["specHash"] = specHash
			if err = r.Update(ctx, &svc); err != nil {
				log.Error(err, "unable to update service with new specHash label")
				return ctrl.Result{}, err
			}
		}
	}

	switch bgd.Status.Phase {
	case "":
		result, err := r.handleEmptyPhase(ctx, &bgd)
		if err != nil {
			return result, err
		}
	case learningv1alpha1.PhasePending:
		result, err := r.handlePendingPhase(ctx, &bgd)
		if err != nil {
			return result, err
		}
	case learningv1alpha1.PhaseDeploying:
		result, err := r.handleDeployingPhase(ctx, &bgd)
		if err != nil {
			return result, err
		}

	case learningv1alpha1.PhasePromoting: // this phase is set by the tester controller when tests pass
		result, err := r.handlePromotingPhase(ctx, &bgd)
		if err != nil {
			return result, err
		}
	}

	retryResult, err := r.retryUpdateOnConflict(ctx, "update BGD status", func() error {
		status := bgd.Status.DeepCopy()
		if err := r.Get(ctx, req.NamespacedName, &bgd); err != nil {
			return err
		}
		bgd.Status = *status
		return r.Status().Update(ctx, &bgd)
	})

	if err != nil {
		// TODO: handle status error
		return retryResult, err
	}

	return ctrl.Result{}, nil
}

// handlePromotingPhase creates active service if it doesn't exist (could happen during the first ever rollout)
func (r *BlueGreenDeploymentReconciler) handlePromotingPhase(ctx context.Context, bgd *learningv1alpha1.BlueGreenDeployment) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	color := "active"
	activeServices, err := r.listServicesForBGD(ctx, color)
	if apierrors.IsNotFound(err) || len(activeServices.Items) == 0 {
		log.Info("Active service does not exist, creating")
		s := r.serviceForBGD(bgd, "active", true)
		err = r.Create(ctx, &s)
		if err != nil {
			log.Error(err, "unable to create active service")
			return ctrl.Result{}, err
		}
	} else if err != nil {
		log.Error(err, "unable to fetch active service for BGD")
		return ctrl.Result{}, err
	}

	active := true
	activeReplicaSets, err := r.listReplicaSetsForBGD(ctx, active)
	if err != nil {
		log.Error(err, "unable to fetch active replicaset for BGD")
		return ctrl.Result{}, err
	}

	promotionLabelName := "aca.com/promotion-status"
	promotionLabelValue := "progressing"
	if len(activeReplicaSets.Items) == 0 || (len(activeReplicaSets.Items) == 1 && activeReplicaSets.Items[0].Labels[promotionLabelName] != promotionLabelValue) {
		color := "active"
		activeRs := r.replicaSetForBGD(bgd, color, active)
		activeRs.Labels[promotionLabelName] = promotionLabelValue
		err = r.Create(ctx, &activeRs)
		if err != nil {
			log.Error(err, "unable to create active replicaset")
			return ctrl.Result{}, err
		}

		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil // requeue to continue promotion in next loop
	}

	var progressingRs appsv1.ReplicaSet
	for _, rs := range activeReplicaSets.Items {
		if rs.Labels[promotionLabelName] == promotionLabelValue {
			progressingRs = rs
			break
		}
	}
	progressingRs.Spec.Template = bgd.Spec.Deployment.Template
	progressingRs.Spec.Replicas = bgd.Spec.Deployment.Replicas
	progressingRs.Labels = r.replicaSetLabel(bgd.Spec.Deployment.Template.Labels, "active", true)

	var depromotingRs appsv1.ReplicaSet
	for _, rs := range activeReplicaSets.Items {
		if rs.Labels[promotionLabelName] == "depromoting" {
			depromotingRs = rs
			break
		}
	}
	if depromotingRs.Labels == nil {
		depromotingRs.Labels = make(map[string]string)
	}
	depromotingRs.Labels[promotionLabelName] = "depromoting"

	active = false
	inactiveRss, err := r.listReplicaSetsForBGD(ctx, active)
	if err != nil {
		log.Error(err, "unable to fetch green replicaset for BGD")
		return ctrl.Result{}, err
	}

	preview := inactiveRss.Items[0]
	replicas := int32(0)
	preview.Spec.Replicas = &replicas

	color = "active"
	activeServices, err = r.listServicesForBGD(ctx, color)
	if err != nil {
		log.Error(err, "unable to fetch active service for BGD")
		return ctrl.Result{}, err
	}

	activeService := activeServices.Items[0]
	activeService.Spec = bgd.Spec.Service.ServiceSpec

	if result, err := r.retryUpdateOnConflict(ctx, "update deployments during promotion", func() error {
		if err := r.Update(ctx, &progressingRs); err != nil {
			return errors.Join(err, promoteProgressingError{})
		}

		if depromotingRs.Name != "" { // could be empty if this is the first ever promotion
			if err := r.Update(ctx, &depromotingRs); err != nil {
				return errors.Join(err, promoteDepromoteUpdateError{})
			}
		}

		if err := r.Update(ctx, &preview); err != nil {
			return errors.Join(err, promotePreviewUpdateError{})
		}

		if err := r.Update(ctx, &activeService); err != nil {
			return errors.Join(err, deployServiceUpdateError{})
		}

		return nil
	}); err != nil {
		switch err.(type) {
		case promoteProgressingError:
			addStatusCondition(
				bgd,
				learningv1alpha1.ConditionPromoting,
				metav1.ConditionFalse,
				learningv1alpha1.ReasonRolloutFailed,
				"Failed to update progressing replicaset during promotion",
			)
		case promoteDepromoteUpdateError:
			addStatusCondition(
				bgd,
				learningv1alpha1.ConditionPromoting,
				metav1.ConditionFalse,
				learningv1alpha1.ReasonRolloutFailed,
				"Failed to update active replicaset during promotion",
			)
		case promotePreviewUpdateError:
			addStatusCondition(
				bgd,
				learningv1alpha1.ConditionPromoting,
				metav1.ConditionFalse,
				learningv1alpha1.ReasonRolloutFailed,
				"Failed to update preview replicaset during promotion",
			)
		case deployServiceUpdateError:
			addStatusCondition(
				bgd,
				learningv1alpha1.ConditionPromoting,
				metav1.ConditionFalse,
				learningv1alpha1.ReasonRolloutFailed,
				"Failed to update active service during promotion",
			)
		default:
			log.Error(err, "unknown error during promotion")

		}
		return result, err
	}

	bgd.Status.Phase = learningv1alpha1.PhaseCleaningUp
	addStatusCondition(
		bgd,
		learningv1alpha1.ConditionPromoting,
		metav1.ConditionTrue,
		learningv1alpha1.ReasonServiceUpdated,
		"Service updated to point to new replicaset, cleaning up old resources",
	)
	return ctrl.Result{}, nil
}

func (r *BlueGreenDeploymentReconciler) handleDeployingPhase(ctx context.Context, bgd *learningv1alpha1.BlueGreenDeployment) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	active := false
	color := "green"
	deploys, err := r.listReplicaSetsForBGD(ctx, active)
	if err != nil {
		log.Error(err, "unable to fetch preview replicaset for BGD")
		return ctrl.Result{}, err
	}

	services, err := r.listServicesForBGD(ctx, color)
	if err != nil {
		log.Error(err, "unable to fetch preview service for BGD")
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

	// Check if we must trigger promotion
	if serviceReady && deploy.Status.ReadyReplicas == *deploy.Spec.Replicas {
		bgd.Status.Phase = learningv1alpha1.PhaseRunTests
		addStatusCondition(
			bgd,
			learningv1alpha1.ConditionRunTests,
			metav1.ConditionTrue,
			learningv1alpha1.ReasonDeploymentCreated,
			"ReplicaSet and Service are ready, proceed with tests",
		)
	}
	return ctrl.Result{}, nil
}

func (r *BlueGreenDeploymentReconciler) handlePendingPhase(ctx context.Context, bgd *learningv1alpha1.BlueGreenDeployment) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("updating preview replicaset")
	active := false
	color := "green"
	deploys, err := r.listReplicaSetsForBGD(ctx, active)
	if err != nil {
		log.Error(err, "unable to fetch active replicaset for BGD")
		return ctrl.Result{}, err
	}

	deploy := deploys.Items[0]
	deploy.Spec.Template = bgd.Spec.Deployment.Template
	deploy.Labels = r.replicaSetLabel(bgd.Spec.Deployment.Template.Labels, color, active)
	deploy.Spec.Replicas = bgd.Spec.Deployment.Replicas
	err = r.Update(ctx, &deploy)
	if err != nil {
		log.Error(err, "unable to update preview replicaset")
		return ctrl.Result{}, err
	}
	log.Info("preview replicaset updated successfully")

	log.Info("creating preview service")
	svcs, err := r.listServicesForBGD(ctx, color)
	if err != nil {
		log.Error(err, "unable to fetch preview service for BGD")
		return ctrl.Result{}, err
	}

	svc := svcs.Items[0]
	svc.Spec = bgd.Spec.Service.ServiceSpec
	svc.Spec.Selector["color"] = color
	svc.Spec.Selector["active"] = strconv.FormatBool(active)
	svc.ObjectMeta.Labels["color"] = color
	err = r.Update(ctx, &svc)
	if err != nil {
		log.Error(err, "unable to update service")
		return ctrl.Result{}, err
	}
	log.Info("preview service updated successfully")

	bgd.Status.Phase = learningv1alpha1.PhaseDeploying
	addStatusCondition(
		bgd,
		learningv1alpha1.ConditionDeploying,
		metav1.ConditionTrue,
		learningv1alpha1.ReasonDeploymentCreated,
		"Deployment and Service created, waiting for pods to be ready",
	)
	return ctrl.Result{}, nil
}

func (r *BlueGreenDeploymentReconciler) handleEmptyPhase(ctx context.Context, bgd *learningv1alpha1.BlueGreenDeployment) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	log.Info("creating preview replicaset")
	active := false
	color := "green"
	rs := r.replicaSetForBGD(bgd, color, active)

	err := r.Create(ctx, &rs)
	if err != nil {
		log.Error(err, "unable to create replicaset")
		return ctrl.Result{}, err
	}

	log.Info("creating green service")
	s := r.serviceForBGD(bgd, color, active)
	err = r.Create(ctx, &s)
	if err != nil {
		log.Error(err, "unable to create service")
		return ctrl.Result{}, err
	}

	bgd.Status.Phase = learningv1alpha1.PhaseDeploying
	addStatusCondition(
		bgd,
		learningv1alpha1.ConditionDeploying,
		metav1.ConditionTrue,
		learningv1alpha1.ReasonDeploymentCreated,
		"Deployment and Service created, waiting for pods to be ready",
	)
	return ctrl.Result{}, nil
}

func (r *BlueGreenDeploymentReconciler) listReplicaSetsForBGD(ctx context.Context, active bool) (appsv1.ReplicaSetList, error) {
	rss := appsv1.ReplicaSetList{}
	labelSelector := client.MatchingLabels{
		"active": strconv.FormatBool(active),
	}
	if err := r.List(ctx, &rss, labelSelector); err != nil {
		return rss, err
	}
	return rss, nil
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

func (r *BlueGreenDeploymentReconciler) replicaSetForBGD(bgd *learningv1alpha1.BlueGreenDeployment, color string, active bool) appsv1.ReplicaSet {
	spec := *bgd.Spec.Deployment.DeepCopy()
	selector := spec.Selector
	if selector.MatchLabels == nil {
		selector.MatchLabels = make(map[string]string)
	}
	selector.MatchLabels["active"] = strconv.FormatBool(active)
	selector.MatchLabels["color"] = color
	spec.Selector = selector

	spec.Template.ObjectMeta.Labels = r.replicaSetLabel(spec.Template.Labels, color, active)

	bgd.Spec.Deployment = spec

	rs := appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: color + bgd.Name + "-", // K8s will append a random suffix
			Namespace:    bgd.Namespace,
			Labels: map[string]string{
				"color":  color,
				"active": strconv.FormatBool(active),
			},
			Annotations: map[string]string{},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(bgd, bgd.GroupVersionKind()),
			},
		},
		Spec: appsv1.ReplicaSetSpec{
			Selector: spec.Selector.DeepCopy(),
			Template: *spec.Template.DeepCopy(),
		},
	}
	return rs
}

func (*BlueGreenDeploymentReconciler) replicaSetLabel(bgdLabels map[string]string, color string, active bool) map[string]string {
	templateLabels := bgdLabels
	if templateLabels == nil {
		templateLabels = make(map[string]string)
	}
	templateLabels["active"] = strconv.FormatBool(active)
	templateLabels["color"] = color
	return templateLabels
}

func (*BlueGreenDeploymentReconciler) serviceForBGD(bgd *learningv1alpha1.BlueGreenDeployment, color string, active bool) corev1.Service {
	svcSpec := bgd.Spec.Service
	namePrefix := "active-"
	if !active {
		namePrefix = "green-"
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
			Annotations: map[string]string{},
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

// SetupWithManager sets up the controller with the Manager.
func (r *BlueGreenDeploymentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&learningv1alpha1.BlueGreenDeployment{}).
		Owns(&appsv1.ReplicaSet{}).
		Owns(&corev1.Service{}).
		WithEventFilter(predicate.Funcs{
			UpdateFunc: func(e event.UpdateEvent) bool {
				switch e.ObjectNew.(type) {
				case *appsv1.ReplicaSet, *corev1.Service:
					return true
				default:
					newBGDObj := e.ObjectNew.(*learningv1alpha1.BlueGreenDeployment)
					oldBGDObj := e.ObjectOld.(*learningv1alpha1.BlueGreenDeployment)
					// Allow reconcile request if status has changed
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
