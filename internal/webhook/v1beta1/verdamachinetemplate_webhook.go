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
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/util/validation/field"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/cluster-api/util/topology"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
)

// SetupVerdaMachineTemplateWebhookWithManager registers the webhook for VerdaMachineTemplate in the manager.
func SetupVerdaMachineTemplateWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &infrav1.VerdaMachineTemplate{}).
		WithValidator(&VerdaMachineTemplateCustomValidator{}).
		WithDefaulter(&VerdaMachineTemplateCustomDefaulter{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-infrastructure-cluster-x-k8s-io-v1beta1-verdamachinetemplate,mutating=true,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=verdamachinetemplates,verbs=create;update,versions=v1beta1,name=mverdamachinetemplate-v1beta1.kb.io,admissionReviewVersions=v1

// VerdaMachineTemplateCustomDefaulter sets defaults on VerdaMachineTemplates.
type VerdaMachineTemplateCustomDefaulter struct{}

// Default implements webhook.CustomDefaulter.
func (d *VerdaMachineTemplateCustomDefaulter) Default(_ context.Context, obj *infrav1.VerdaMachineTemplate) error {
	defaultMachineSpec(&obj.Spec.Template.Spec)
	return nil
}

// +kubebuilder:webhook:path=/validate-infrastructure-cluster-x-k8s-io-v1beta1-verdamachinetemplate,mutating=false,failurePolicy=fail,sideEffects=None,groups=infrastructure.cluster.x-k8s.io,resources=verdamachinetemplates,verbs=create;update,versions=v1beta1,name=vverdamachinetemplate-v1beta1.kb.io,admissionReviewVersions=v1

// VerdaMachineTemplateCustomValidator validates VerdaMachineTemplates. The
// template spec is immutable, except for server-side-apply dry runs issued by
// the Cluster API topology controller, which the provider contract requires
// to be allowed.
type VerdaMachineTemplateCustomValidator struct{}

// ValidateCreate implements webhook.CustomValidator.
func (v *VerdaMachineTemplateCustomValidator) ValidateCreate(_ context.Context, obj *infrav1.VerdaMachineTemplate) (admission.Warnings, error) {
	return nil, aggregate(obj, validateMachineSpec(&obj.Spec.Template.Spec, field.NewPath("spec", "template", "spec")))
}

// ValidateUpdate implements webhook.CustomValidator.
func (v *VerdaMachineTemplateCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj *infrav1.VerdaMachineTemplate) (admission.Warnings, error) {
	req, err := admission.RequestFromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("expected an admission.Request inside context: %w", err)
	}
	errs := validateMachineSpec(&newObj.Spec.Template.Spec, field.NewPath("spec", "template", "spec"))
	if !topology.IsDryRunRequest(req, newObj) && !reflect.DeepEqual(oldObj.Spec.Template.Spec, newObj.Spec.Template.Spec) {
		errs = append(errs, field.Forbidden(field.NewPath("spec", "template", "spec"), "VerdaMachineTemplate spec is immutable; create a new template to change machines"))
	}
	return nil, aggregate(newObj, errs)
}

// ValidateDelete implements webhook.CustomValidator.
func (v *VerdaMachineTemplateCustomValidator) ValidateDelete(_ context.Context, _ *infrav1.VerdaMachineTemplate) (admission.Warnings, error) {
	return nil, nil
}
