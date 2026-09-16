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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// VerdaMachineTemplateSpec defines the desired state of VerdaMachineTemplate.
type VerdaMachineTemplateSpec struct {
	// template is the VerdaMachine template.
	// +required
	Template VerdaMachineTemplateResource `json:"template"`
}

// VerdaMachineTemplateResource describes the data needed to create a VerdaMachine from a template.
type VerdaMachineTemplateResource struct {
	// metadata is the standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	ObjectMeta clusterv1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec is the specification of the desired behavior of the machine.
	// +required
	Spec VerdaMachineSpec `json:"spec"`
}

// VerdaMachineTemplateStatus defines the observed state of VerdaMachineTemplate.
type VerdaMachineTemplateStatus struct {
	// capacity defines the resource capacity for this machine.
	// This value is used for autoscaling from zero operations as defined in:
	// https://github.com/kubernetes-sigs/cluster-api/blob/main/docs/proposals/20210310-opt-in-autoscaling-from-zero.md
	// It is filled in by the provider from the Verda instance type catalog.
	// +optional
	Capacity corev1.ResourceList `json:"capacity,omitempty"`

	// nodeInfo describes the architecture and operating system of nodes
	// created from this template, for autoscaling from zero.
	// +optional
	NodeInfo NodeInfo `json:"nodeInfo,omitempty,omitzero"`

	// conditions represents the observations of the template's current state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// NodeInfo describes the nodes a template produces.
// +kubebuilder:validation:MinProperties=1
type NodeInfo struct {
	// architecture is the CPU architecture of the node.
	// +optional
	// +kubebuilder:validation:Enum=amd64;arm64;s390x;ppc64le
	Architecture string `json:"architecture,omitempty"`

	// operatingSystem is the operating system of the node.
	// +optional
	// +kubebuilder:validation:Enum=linux;windows
	OperatingSystem string `json:"operatingSystem,omitempty"`
}

// CapacityReadyCondition reports whether status.capacity was resolved from the
// Verda instance type catalog.
const CapacityReadyCondition = "CapacityReady"

// GetConditions returns the set of conditions for this object.
func (t *VerdaMachineTemplate) GetConditions() []metav1.Condition {
	return t.Status.Conditions
}

// SetConditions sets the conditions on this object.
func (t *VerdaMachineTemplate) SetConditions(conditions []metav1.Condition) {
	t.Status.Conditions = conditions
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=verdamachinetemplates,shortName=vmt,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time duration since creation of VerdaMachineTemplate"

// VerdaMachineTemplate is the Schema for the verdamachinetemplates API.
type VerdaMachineTemplate struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard object's metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec is the desired state of VerdaMachineTemplate.
	// +optional
	Spec VerdaMachineTemplateSpec `json:"spec,omitempty,omitzero"`

	// status is the observed state of VerdaMachineTemplate.
	// +optional
	Status VerdaMachineTemplateStatus `json:"status,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// VerdaMachineTemplateList contains a list of VerdaMachineTemplate.
type VerdaMachineTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VerdaMachineTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(SchemeGroupVersion, &VerdaMachineTemplate{}, &VerdaMachineTemplateList{})
		return nil
	})
}
