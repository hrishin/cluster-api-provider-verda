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

// VerdaClusterTemplate: the VerdaCluster template a ClusterClass references.

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

// VerdaClusterTemplateSpec defines the desired state of VerdaClusterTemplate.
type VerdaClusterTemplateSpec struct {
	// template is the VerdaCluster template.
	// +required
	Template VerdaClusterTemplateResource `json:"template"`
}

// VerdaClusterTemplateResource describes the data needed to create a VerdaCluster from a template.
type VerdaClusterTemplateResource struct {
	// metadata is the standard object's metadata.
	// More info: https://git.k8s.io/community/contributors/devel/sig-architecture/api-conventions.md#metadata
	// +optional
	ObjectMeta clusterv1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec is the specification of the desired behavior of the cluster.
	// +required
	Spec VerdaClusterSpec `json:"spec"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=verdaclustertemplates,shortName=vct,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time duration since creation of VerdaClusterTemplate"

// VerdaClusterTemplate is the Schema for the verdaclustertemplates API.
type VerdaClusterTemplate struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard object's metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec is the desired state of VerdaClusterTemplate.
	// +optional
	Spec VerdaClusterTemplateSpec `json:"spec,omitempty,omitzero"`
}

// +kubebuilder:object:root=true

// VerdaClusterTemplateList contains a list of VerdaClusterTemplate.
type VerdaClusterTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VerdaClusterTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(SchemeGroupVersion, &VerdaClusterTemplate{}, &VerdaClusterTemplateList{})
		return nil
	})
}
