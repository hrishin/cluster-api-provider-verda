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

package v1beta1

import (
	"context"

	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
)

// SetupVerdaClusterWebhookWithManager registers the webhook for VerdaCluster in the manager.
func SetupVerdaClusterWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &infrav1.VerdaCluster{}).
		WithValidator(&VerdaClusterCustomValidator{}).
		WithDefaulter(&VerdaClusterCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-infrastructure-cluster-x-k8s-io-v1beta1-verdacluster,mutating=true,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=verdaclusters,verbs=create;update,versions=v1beta1,name=mverdacluster-v1beta1.kb.io,admissionReviewVersions=v1

// VerdaClusterCustomDefaulter sets defaults on VerdaClusters.
type VerdaClusterCustomDefaulter struct{}

// Default implements webhook.CustomDefaulter.
func (d *VerdaClusterCustomDefaulter) Default(_ context.Context, obj *infrav1.VerdaCluster) error {
	defaultClusterSpec(&obj.Spec)
	return nil
}

func defaultClusterSpec(spec *infrav1.VerdaClusterSpec) {
	if spec.ControlPlaneEndpoint.Host != "" && spec.ControlPlaneEndpoint.Port == 0 {
		spec.ControlPlaneEndpoint.Port = 6443
	}
	if spec.ControlPlaneLoadBalancer.Enabled != nil && *spec.ControlPlaneLoadBalancer.Enabled {
		if spec.ControlPlaneLoadBalancer.InstanceType == "" {
			spec.ControlPlaneLoadBalancer.InstanceType = infrav1.DefaultLoadBalancerInstanceType
		}
		if spec.ControlPlaneLoadBalancer.Image == "" {
			spec.ControlPlaneLoadBalancer.Image = infrav1.DefaultLoadBalancerImage
		}
	}
}

// +kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1beta1-verdacluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=verdaclusters,verbs=create;update,versions=v1beta1,name=vverdacluster-v1beta1.kb.io,admissionReviewVersions=v1

// VerdaClusterCustomValidator validates VerdaClusters.
type VerdaClusterCustomValidator struct{}

// ValidateCreate implements webhook.CustomValidator.
func (v *VerdaClusterCustomValidator) ValidateCreate(_ context.Context, obj *infrav1.VerdaCluster) (admission.Warnings, error) {
	return nil, aggregate(obj, validateClusterSpec(&obj.Spec, field.NewPath("spec")))
}

// ValidateUpdate implements webhook.CustomValidator.
func (v *VerdaClusterCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *infrav1.VerdaCluster) (admission.Warnings, error) {
	errs := validateClusterSpec(&newObj.Spec, field.NewPath("spec"))
	errs = append(errs, validateClusterSpecUpdate(&oldObj.Spec, &newObj.Spec, field.NewPath("spec"))...)
	return nil, aggregate(newObj, errs)
}

// ValidateDelete implements webhook.CustomValidator.
func (v *VerdaClusterCustomValidator) ValidateDelete(_ context.Context, _ *infrav1.VerdaCluster) (admission.Warnings, error) {
	return nil, nil
}
