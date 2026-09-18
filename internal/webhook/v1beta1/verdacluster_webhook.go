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

// Defaulting and validation webhook for VerdaCluster.

package v1beta1

import (
	"context"

	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
)

func SetupVerdaClusterWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &infrav1.VerdaCluster{}).
		WithValidator(&VerdaClusterCustomValidator{}).
		WithDefaulter(&VerdaClusterCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-infrastructure-cluster-x-k8s-io-v1beta1-verdacluster,mutating=true,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=verdaclusters,verbs=create;update,versions=v1beta1,name=mverdacluster-v1beta1.kb.io,admissionReviewVersions=v1

type VerdaClusterCustomDefaulter struct{}

func (d *VerdaClusterCustomDefaulter) Default(_ context.Context, obj *infrav1.VerdaCluster) error {
	defaultClusterSpec(&obj.Spec)
	return nil
}

func defaultClusterSpec(spec *infrav1.VerdaClusterSpec) {
	if spec.ControlPlaneEndpoint.Host != "" && spec.ControlPlaneEndpoint.Port == 0 {
		spec.ControlPlaneEndpoint.Port = 6443
	}
	defaultLoadBalancer(&spec.ControlPlaneLoadBalancer)
	defaultLoadBalancer(&spec.ServiceLoadBalancer)
}

func defaultLoadBalancer(lb *infrav1.ControlPlaneLoadBalancer) {
	if lb.Enabled == nil || !*lb.Enabled {
		return
	}
	if lb.InstanceType == "" {
		lb.InstanceType = infrav1.DefaultLoadBalancerInstanceType
	}
	if lb.Image == "" {
		lb.Image = infrav1.DefaultLoadBalancerImage
	}
}

// +kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1beta1-verdacluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=verdaclusters,verbs=create;update,versions=v1beta1,name=vverdacluster-v1beta1.kb.io,admissionReviewVersions=v1

type VerdaClusterCustomValidator struct{}

func (v *VerdaClusterCustomValidator) ValidateCreate(_ context.Context, obj *infrav1.VerdaCluster) (admission.Warnings, error) {
	return nil, aggregate(obj, validateClusterSpec(&obj.Spec, field.NewPath("spec")))
}

func (v *VerdaClusterCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *infrav1.VerdaCluster) (admission.Warnings, error) {
	errs := validateClusterSpec(&newObj.Spec, field.NewPath("spec"))
	errs = append(errs, validateClusterSpecUpdate(&oldObj.Spec, &newObj.Spec, field.NewPath("spec"))...)
	return nil, aggregate(newObj, errs)
}

func (v *VerdaClusterCustomValidator) ValidateDelete(_ context.Context, _ *infrav1.VerdaCluster) (admission.Warnings, error) {
	return nil, nil
}
