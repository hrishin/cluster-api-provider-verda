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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

const (
	// ClusterFinalizer allows VerdaClusterReconciler to clean up Verda
	// resources associated with a VerdaCluster before removing it from the
	// apiserver.
	ClusterFinalizer = "verdacluster.infrastructure.cluster.x-k8s.io"
)

// VerdaCluster condition types.
const (
	// ControlPlaneEndpointReadyCondition reports whether a control plane endpoint
	// is available in spec.controlPlaneEndpoint.
	ControlPlaneEndpointReadyCondition = "ControlPlaneEndpointReady"
)

// VerdaClusterSpec defines the desired state of VerdaCluster.
type VerdaClusterSpec struct {
	// location is the Verda location code (e.g. FIN-01, FIN-02, FIN-03) in which
	// all machines of the cluster are created.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Location string `json:"location"`

	// controlPlaneEndpoint represents the endpoint used to communicate with the control plane.
	//
	// Verda does not provide a managed load balancer, so the endpoint must be
	// supplied by the user: typically a DNS name pointing at the control plane
	// machine(s), or the address of a load balancer managed outside of Cluster API.
	// +optional
	ControlPlaneEndpoint clusterv1.APIEndpoint `json:"controlPlaneEndpoint,omitempty,omitzero"`
}

// VerdaClusterStatus defines the observed state of VerdaCluster.
type VerdaClusterStatus struct {
	// initialization provides observations of the VerdaCluster initialization process.
	// NOTE: Fields in this struct are part of the Cluster API contract and are used to orchestrate initial Cluster provisioning.
	// +optional
	Initialization VerdaClusterInitializationStatus `json:"initialization,omitempty,omitzero"`

	// conditions represents the observations of a VerdaCluster's current state.
	// Known condition types are Ready, ControlPlaneEndpointReady, Paused.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=32
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// VerdaClusterInitializationStatus provides observations of the VerdaCluster initialization process.
// +kubebuilder:validation:MinProperties=1
type VerdaClusterInitializationStatus struct {
	// provisioned is true when the infrastructure provider reports that the Cluster's infrastructure is fully provisioned.
	// NOTE: this field is part of the Cluster API contract, and it is used to orchestrate initial Cluster provisioning.
	// +optional
	Provisioned *bool `json:"provisioned,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=verdaclusters,shortName=vcl,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".metadata.labels.cluster\\.x-k8s\\.io/cluster-name",description="Cluster to which this VerdaCluster belongs"
// +kubebuilder:printcolumn:name="Provisioned",type="boolean",JSONPath=".status.initialization.provisioned",description="Cluster infrastructure is provisioned"
// +kubebuilder:printcolumn:name="Endpoint",type="string",JSONPath=".spec.controlPlaneEndpoint.host",description="API endpoint",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time duration since creation of VerdaCluster"

// VerdaCluster is the Schema for the verdaclusters API.
type VerdaCluster struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard object's metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec is the desired state of VerdaCluster.
	// +optional
	Spec VerdaClusterSpec `json:"spec,omitempty,omitzero"`

	// status is the observed state of VerdaCluster.
	// +optional
	Status VerdaClusterStatus `json:"status,omitempty,omitzero"`
}

// GetConditions returns the set of conditions for this object.
func (c *VerdaCluster) GetConditions() []metav1.Condition {
	return c.Status.Conditions
}

// SetConditions sets the conditions on this object.
func (c *VerdaCluster) SetConditions(conditions []metav1.Condition) {
	c.Status.Conditions = conditions
}

// +kubebuilder:object:root=true

// VerdaClusterList contains a list of VerdaCluster.
type VerdaClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VerdaCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(SchemeGroupVersion, &VerdaCluster{}, &VerdaClusterList{})
		return nil
	})
}
