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

// VerdaCluster: cluster-level Verda infrastructure — control plane endpoint, haproxy and envoy load balancers, failure domains.

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

	// LoadBalancerReadyCondition reports whether the provider-managed control
	// plane load balancer instance is running and its backends are in sync.
	LoadBalancerReadyCondition = "LoadBalancerReady"

	// ServiceLoadBalancerReadyCondition reports whether the provider-managed
	// service load balancer instance is running and its address and keys have
	// been handed to the workload cluster.
	ServiceLoadBalancerReadyCondition = "ServiceLoadBalancerReady"
)

// ServiceLoadBalancerSecretName is the Secret in the workload cluster's
// kube-system namespace through which the cloud controller manager learns the
// service load balancer's address and SSH keys.
const ServiceLoadBalancerSecretName = "verda-service-lb"

// Defaults for the provider-managed control plane load balancer.
const (
	DefaultLoadBalancerInstanceType = "CPU.4V.16G"
	DefaultLoadBalancerImage        = "24.04.base"
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
	// Verda does not provide a managed load balancer. Either enable
	// controlPlaneLoadBalancer, in which case the provider fills this in with
	// the address of the load balancer instance it creates, or supply it: a DNS
	// name pointing at the control plane machine(s), or the address of a load
	// balancer managed outside of Cluster API.
	// +optional
	ControlPlaneEndpoint clusterv1.APIEndpoint `json:"controlPlaneEndpoint,omitempty,omitzero"`

	// identityRef references a Secret in the same namespace holding the Verda
	// API credentials for this cluster, with keys client-id, client-secret and
	// optionally base-url. When unset the credentials the manager was started
	// with are used.
	// +optional
	IdentityRef *IdentityReference `json:"identityRef,omitempty"`

	// controlPlaneLoadBalancer configures a provider-managed load balancer in
	// front of the control plane: a Verda instance running haproxy in TCP mode
	// whose backends are kept in sync with the control plane machines.
	// +optional
	ControlPlaneLoadBalancer ControlPlaneLoadBalancer `json:"controlPlaneLoadBalancer,omitempty,omitzero"`

	// serviceLoadBalancer configures a provider-managed load balancer for
	// Services of type LoadBalancer: a Verda instance running envoy whose
	// listeners are managed by the cloud controller manager in the workload
	// cluster. TCP and UDP are forwarded as-is (TLS passes through).
	// +optional
	ServiceLoadBalancer ControlPlaneLoadBalancer `json:"serviceLoadBalancer,omitempty,omitzero"`
}

// IdentityReference points at a Secret holding Verda API credentials.
type IdentityReference struct {
	// name is the name of the Secret.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`
}

// ControlPlaneLoadBalancer describes the provider-managed control plane load balancer.
// +kubebuilder:validation:MinProperties=1
type ControlPlaneLoadBalancer struct {
	// enabled turns on the provider-managed load balancer.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// instanceType is the Verda instance type of the load balancer. Defaults to CPU.4V.16G.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	InstanceType string `json:"instanceType,omitempty"`

	// image is the Verda image the load balancer boots from. It must be a
	// Debian-based image with apt access. Defaults to 24.04.base (Ubuntu 24.04).
	// +optional
	// +kubebuilder:validation:MaxLength=256
	Image string `json:"image,omitempty"`

	// sshKeyIDs are additional Verda SSH keys to inject into the load balancer
	// instance, for troubleshooting. The provider always injects its own key.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=128
	SSHKeyIDs []string `json:"sshKeyIDs,omitempty"`
}

// VerdaClusterStatus defines the observed state of VerdaCluster.
type VerdaClusterStatus struct {
	// initialization provides observations of the VerdaCluster initialization process.
	// NOTE: Fields in this struct are part of the Cluster API contract and are used to orchestrate initial Cluster provisioning.
	// +optional
	Initialization VerdaClusterInitializationStatus `json:"initialization,omitempty,omitzero"`

	// conditions represents the observations of a VerdaCluster's current state.
	// Known condition types are Ready, ControlPlaneEndpointReady, LoadBalancerReady, Paused.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=32
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// loadBalancer holds the state of the provider-managed control plane load balancer.
	// +optional
	LoadBalancer LoadBalancerStatus `json:"loadBalancer,omitempty,omitzero"`

	// serviceLoadBalancer holds the state of the provider-managed service load balancer.
	// +optional
	ServiceLoadBalancer LoadBalancerStatus `json:"serviceLoadBalancer,omitempty,omitzero"`

	// failureDomains is a list of failure domain objects synced from the infrastructure provider.
	// A VerdaCluster lives in a single location, which is its only failure domain.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=100
	FailureDomains []clusterv1.FailureDomain `json:"failureDomains,omitempty"`
}

// LoadBalancerStatus describes the provider-managed control plane load balancer.
// +kubebuilder:validation:MinProperties=1
type LoadBalancerStatus struct {
	// instanceID is the identifier of the Verda instance running haproxy.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	InstanceID string `json:"instanceID,omitempty"`

	// startupScriptID is the identifier of the load balancer's startup script.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	StartupScriptID string `json:"startupScriptID,omitempty"`

	// address is the public IP of the load balancer instance.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	Address string `json:"address,omitempty"`

	// backends are the control plane addresses currently configured in haproxy.
	// Unused for the service load balancer, whose listeners live in the workload cluster.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:items:MaxLength=64
	Backends []string `json:"backends,omitempty"`

	// secretPublished is true once the address and SSH keys have been written
	// to the workload cluster (service load balancer only).
	// +optional
	SecretPublished *bool `json:"secretPublished,omitempty"`
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
// +kubebuilder:printcolumn:name="Endpoint",type="string",JSONPath=".spec.controlPlaneEndpoint.host",description="API endpoint"
// +kubebuilder:printcolumn:name="LB",type="string",JSONPath=".status.loadBalancer.instanceID",description="Load balancer instance",priority=1
// +kubebuilder:printcolumn:name="Backends",type="string",JSONPath=".status.loadBalancer.backends",description="Load balancer backends",priority=1
// +kubebuilder:printcolumn:name="ServiceLB",type="string",JSONPath=".status.serviceLoadBalancer.address",description="Service load balancer address",priority=1
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

func (c *VerdaCluster) IdentitySecretName() string {
	if c.Spec.IdentityRef == nil {
		return ""
	}
	return c.Spec.IdentityRef.Name
}

func (c *VerdaCluster) LoadBalancerEnabled() bool {
	return c.Spec.ControlPlaneLoadBalancer.Enabled != nil && *c.Spec.ControlPlaneLoadBalancer.Enabled
}

func (c *VerdaCluster) ServiceLoadBalancerEnabled() bool {
	return c.Spec.ServiceLoadBalancer.Enabled != nil && *c.Spec.ServiceLoadBalancer.Enabled
}

func (c *VerdaCluster) GetConditions() []metav1.Condition {
	return c.Status.Conditions
}

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
