/*
Copyright 2026 The Verda CAPI Authors.

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

// VerdaMachineTemplate reconciler filling status.capacity and nodeInfo from the instance type catalog.

package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/conditions"
	"sigs.k8s.io/cluster-api/util/patch"
	"sigs.k8s.io/cluster-api/util/predicates"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
	"github.com/hrishin/verda-capi/internal/cloud"
)

const capacityResyncInterval = 12 * time.Hour

const gpuResourceName corev1.ResourceName = "nvidia.com/gpu"

type VerdaMachineTemplateReconciler struct {
	client.Client
	CloudFactory     cloud.Factory
	WatchFilterValue string
}

// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=verdamachinetemplates,verbs=get;list;watch
// +kubebuilder:rbac:groups=infrastructure.cluster.x-k8s.io,resources=verdamachinetemplates/status,verbs=get;update;patch

func (r *VerdaMachineTemplateReconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reterr error) {
	log := ctrl.LoggerFrom(ctx)

	template := &infrav1.VerdaMachineTemplate{}
	if err := r.Get(ctx, req.NamespacedName, template); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if !template.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	patchHelper, err := patch.NewHelper(template, r.Client)
	if err != nil {
		return ctrl.Result{}, err
	}
	defer func() {
		if err := patchHelper.Patch(ctx, template, patch.WithOwnedConditions{Conditions: []string{infrav1.CapacityReadyCondition}}); err != nil {
			reterr = kerrorsJoin(reterr, err)
		}
	}()

	identity := cloud.Identity{Namespace: template.Namespace}
	if clusterName, ok := template.Labels[clusterv1.ClusterNameLabel]; ok {
		cluster := &clusterv1.Cluster{}
		if err := r.Get(ctx, client.ObjectKey{Namespace: template.Namespace, Name: clusterName}, cluster); err == nil && cluster.Spec.InfrastructureRef.IsDefined() {
			verdaCluster := &infrav1.VerdaCluster{}
			if err := r.Get(ctx, client.ObjectKey{Namespace: template.Namespace, Name: cluster.Spec.InfrastructureRef.Name}, verdaCluster); err == nil {
				identity.SecretName = verdaCluster.IdentitySecretName()
			}
		}
	}
	verdaClient, err := r.CloudFactory.ClientFor(ctx, identity)
	if err != nil {
		setCapacityNotReady(template, "CredentialsUnavailable", err.Error())
		return ctrl.Result{}, err
	}

	instanceType := template.Spec.Template.Spec.InstanceType
	info, err := verdaClient.GetInstanceType(ctx, instanceType)
	if err != nil {
		if errors.Is(err, cloud.ErrNotFound) {
			setCapacityNotReady(template, "UnknownInstanceType", fmt.Sprintf("instance type %q is not in the Verda catalog", instanceType))
			log.Info("Instance type not found in the Verda catalog", "instanceType", instanceType)
			return ctrl.Result{RequeueAfter: capacityResyncInterval}, nil
		}
		setCapacityNotReady(template, "CatalogUnavailable", err.Error())
		return ctrl.Result{}, err
	}

	template.Status.Capacity = capacityFor(info)
	template.Status.NodeInfo = infrav1.NodeInfo{Architecture: "amd64", OperatingSystem: "linux"}
	conditions.Set(template, metav1.Condition{Type: infrav1.CapacityReadyCondition, Status: metav1.ConditionTrue, Reason: clusterv1.ReadyReason})
	return ctrl.Result{RequeueAfter: capacityResyncInterval}, nil
}

func capacityFor(info *cloud.InstanceTypeInfo) corev1.ResourceList {
	capacity := corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewQuantity(int64(info.CPUs), resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(int64(info.MemoryGB)<<30, resource.BinarySI),
	}
	if info.GPUs > 0 {
		capacity[gpuResourceName] = *resource.NewQuantity(int64(info.GPUs), resource.DecimalSI)
	}
	return capacity
}

func setCapacityNotReady(template *infrav1.VerdaMachineTemplate, reason, message string) {
	conditions.Set(template, metav1.Condition{Type: infrav1.CapacityReadyCondition, Status: metav1.ConditionFalse, Reason: reason, Message: message})
}

func (r *VerdaMachineTemplateReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager, options controller.Options) error {
	predicateLog := ctrl.LoggerFrom(ctx).WithValues("controller", "verdamachinetemplate")
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1.VerdaMachineTemplate{}).
		WithOptions(options).
		WithEventFilter(predicates.ResourceHasFilterLabel(mgr.GetScheme(), predicateLog, r.WatchFilterValue)).
		Named("verdamachinetemplate").
		Complete(r)
}
