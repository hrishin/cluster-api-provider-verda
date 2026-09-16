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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
)

// SetupVerdaMachineWebhookWithManager registers the webhook for VerdaMachine in the manager.
func SetupVerdaMachineWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &infrav1.VerdaMachine{}).
		WithValidator(&VerdaMachineCustomValidator{}).
		WithDefaulter(&VerdaMachineCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-infrastructure-cluster-x-k8s-io-v1beta1-verdamachine,mutating=true,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=verdamachines,verbs=create;update,versions=v1beta1,name=mverdamachine-v1beta1.kb.io,admissionReviewVersions=v1

// VerdaMachineCustomDefaulter sets defaults on VerdaMachines.
type VerdaMachineCustomDefaulter struct{}

// Default implements webhook.CustomDefaulter.
func (d *VerdaMachineCustomDefaulter) Default(_ context.Context, obj *infrav1.VerdaMachine) error {
	defaultMachineSpec(&obj.Spec)
	return nil
}

func defaultMachineSpec(spec *infrav1.VerdaMachineSpec) {
	if spec.Spot != nil && *spec.Spot && spec.Contract == "" {
		spec.Contract = "SPOT"
	}
}

// +kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1beta1-verdamachine,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=verdamachines,verbs=create;update,versions=v1beta1,name=vverdamachine-v1beta1.kb.io,admissionReviewVersions=v1

// VerdaMachineCustomValidator validates VerdaMachines.
type VerdaMachineCustomValidator struct{}

// ValidateCreate implements webhook.CustomValidator.
func (v *VerdaMachineCustomValidator) ValidateCreate(_ context.Context, obj *infrav1.VerdaMachine) (admission.Warnings, error) {
	return nil, aggregate(obj, validateMachineSpec(&obj.Spec, field.NewPath("spec")))
}

// ValidateUpdate implements webhook.CustomValidator.
func (v *VerdaMachineCustomValidator) ValidateUpdate(_ context.Context, oldObj, newObj *infrav1.VerdaMachine) (admission.Warnings, error) {
	errs := validateMachineSpec(&newObj.Spec, field.NewPath("spec"))
	errs = append(errs, validateMachineSpecUpdate(&oldObj.Spec, &newObj.Spec, field.NewPath("spec"))...)
	return nil, aggregate(newObj, errs)
}

// ValidateDelete implements webhook.CustomValidator.
func (v *VerdaMachineCustomValidator) ValidateDelete(_ context.Context, _ *infrav1.VerdaMachine) (admission.Warnings, error) {
	return nil, nil
}

// aggregate turns a field error list into an admission error for obj.
func aggregate(obj client.Object, errs field.ErrorList) error {
	if len(errs) == 0 {
		return nil
	}
	return apierrors.NewInvalid(obj.GetObjectKind().GroupVersionKind().GroupKind(), obj.GetName(), errs)
}
