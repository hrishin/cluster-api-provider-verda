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

// VerdaMachine: one Verda instance backing a Cluster API Machine.

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
)

const (
	// MachineFinalizer allows VerdaMachineReconciler to clean up the Verda
	// instance associated with a VerdaMachine before removing it from the apiserver.
	MachineFinalizer = "verdamachine.infrastructure.cluster.x-k8s.io"

	// ProviderIDPrefix is the prefix of provider IDs set on VerdaMachines and
	// expected on Nodes: verda://<hostname>.
	//
	// The hostname rather than the Verda instance ID is used because the
	// startup script carrying the bootstrap data (and therefore the kubelet's
	// --provider-id) has to exist before the instance, and Verda exposes no
	// metadata service the node could query for its own ID.
	ProviderIDPrefix = "verda://"
)

func ProviderID(hostname string) string {
	return ProviderIDPrefix + hostname
}

// VerdaMachine condition types.
const (
	// InstanceReadyCondition reports whether the backing Verda instance is running.
	InstanceReadyCondition = "InstanceReady"

	// OSVolumeReadyCondition reports whether the per-machine clone of spec.osVolumeID is available.
	OSVolumeReadyCondition = "OSVolumeReady"
)

// VerdaMachineSpec defines the desired state of VerdaMachine.
// +kubebuilder:validation:XValidation:rule="has(self.image) != has(self.osVolumeID)",message="exactly one of image or osVolumeID must be set"
type VerdaMachineSpec struct {
	// providerID must match the provider ID as seen on the node object corresponding to this machine.
	// It has the format verda://<hostname>, where hostname is the name of the VerdaMachine; the
	// kubelet must be started with --provider-id=verda://{{ ds.meta_data.hostname }} (the placeholder
	// is resolved by this provider when the bootstrap data is turned into a startup script).
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=512
	ProviderID string `json:"providerID,omitempty"`

	// instanceType is the Verda instance type to create, e.g. 1V100.6V or CPU.4V.16G.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	InstanceType string `json:"instanceType"`

	// image is the Verda image type to boot the instance from, e.g. 24.04.base or 24.04.cuda13.2.docker (GET /v1/images lists them).
	// kubeadm, kubelet and containerd must be installed by the bootstrap data unless
	// already present in the image. Exactly one of image and osVolumeID must be set.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	Image string `json:"image,omitempty"`

	// osVolumeID is the ID, or the exact name, of a detached Verda OS volume to
	// boot from, typically a node image built with image-builder that already
	// contains kubeadm. The volume is cloned for each machine and the clone is
	// deleted together with the instance. It must live in the cluster's
	// location. Exactly one of image and osVolumeID must be set.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	OSVolumeID string `json:"osVolumeID,omitempty"`

	// sshKeyIDs are identifiers of Verda SSH keys to inject into the instance.
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=128
	SSHKeyIDs []string `json:"sshKeyIDs,omitempty"`

	// osVolumeSizeGB is the size of the OS volume in GB when booting from image.
	// When unset, the Verda default for the image is used. Ignored with osVolumeID
	// (a clone keeps the size of its source).
	// +optional
	// +kubebuilder:validation:Minimum=1
	OSVolumeSizeGB int32 `json:"osVolumeSizeGB,omitempty"`

	// contract selects the Verda billing contract for the instance.
	// +optional
	// +kubebuilder:validation:Enum=PAY_AS_YOU_GO;LONG_TERM;SPOT
	Contract string `json:"contract,omitempty"`

	// spot requests a spot instance. Spot instances can be discontinued by Verda
	// at any time; the corresponding Machine is then reported as failed.
	// +optional
	Spot *bool `json:"spot,omitempty"`

	// spotDiscontinuePolicy is what happens to the machine's volumes when Verda
	// discontinues a spot instance: keep_detached, move_to_trash or
	// delete_permanently. Defaults to delete_permanently because the machine is
	// replaced rather than resumed.
	// +optional
	// +kubebuilder:validation:Enum=keep_detached;move_to_trash;delete_permanently
	SpotDiscontinuePolicy string `json:"spotDiscontinuePolicy,omitempty"`

	// additionalVolumes are data volumes created with the instance, attached
	// to it and deleted together with it. They are not formatted or mounted;
	// use KubeadmConfig diskSetup/mounts or preKubeadmCommands for that.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=8
	AdditionalVolumes []AdditionalVolume `json:"additionalVolumes,omitempty"`
}

// AdditionalVolume describes a data volume to create with the instance.
type AdditionalVolume struct {
	// name identifies the volume; the Verda volume is named <machine>-<name>.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=32
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`

	// sizeGB is the size of the volume in GB.
	// +required
	// +kubebuilder:validation:Minimum=1
	SizeGB int32 `json:"sizeGB"`

	// type is the Verda volume type. Defaults to NVMe.
	// +optional
	// +kubebuilder:validation:Enum=NVMe;HDD;NVMe_Shared;HDD_Shared
	Type string `json:"type,omitempty"`
}

// VerdaMachineStatus defines the observed state of VerdaMachine.
type VerdaMachineStatus struct {
	// initialization provides observations of the VerdaMachine initialization process.
	// NOTE: Fields in this struct are part of the Cluster API contract and are used to orchestrate initial Machine provisioning.
	// +optional
	Initialization VerdaMachineInitializationStatus `json:"initialization,omitempty,omitzero"`

	// conditions represents the observations of a VerdaMachine's current state.
	// Known condition types are Ready, InstanceReady, Paused.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=32
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// addresses contains the associated addresses for the machine.
	// +optional
	// +kubebuilder:validation:MaxItems=32
	Addresses []clusterv1.MachineAddress `json:"addresses,omitempty"`

	// failureDomain is the failure domain the instance is running in.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	FailureDomain string `json:"failureDomain,omitempty"`

	// instanceID is the identifier of the backing Verda instance.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	InstanceID string `json:"instanceID,omitempty"`

	// instanceState is the last observed state of the backing Verda instance.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	InstanceState string `json:"instanceState,omitempty"`

	// osVolumeID is the identifier of the per-machine clone of spec.osVolumeID.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	OSVolumeID string `json:"osVolumeID,omitempty"`

	// startupScriptID is the identifier of the Verda startup script that carries
	// the bootstrap data for this machine. It is deleted together with the instance.
	// +optional
	// +kubebuilder:validation:MaxLength=128
	StartupScriptID string `json:"startupScriptID,omitempty"`
}

// VerdaMachineInitializationStatus provides observations of the VerdaMachine initialization process.
// +kubebuilder:validation:MinProperties=1
type VerdaMachineInitializationStatus struct {
	// provisioned is true when the infrastructure provider reports that the Machine's infrastructure is fully provisioned.
	// NOTE: this field is part of the Cluster API contract, and it is used to orchestrate initial Machine provisioning.
	// +optional
	Provisioned *bool `json:"provisioned,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:path=verdamachines,shortName=vm,scope=Namespaced,categories=cluster-api
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Cluster",type="string",JSONPath=".metadata.labels.cluster\\.x-k8s\\.io/cluster-name",description="Cluster to which this VerdaMachine belongs"
// +kubebuilder:printcolumn:name="Machine",type="string",JSONPath=".metadata.ownerReferences[?(@.kind==\"Machine\")].name",description="Machine object which owns this VerdaMachine"
// +kubebuilder:printcolumn:name="Provisioned",type="boolean",JSONPath=".status.initialization.provisioned",description="Machine infrastructure is provisioned"
// +kubebuilder:printcolumn:name="State",type="string",JSONPath=".status.instanceState",description="Verda instance state"
// +kubebuilder:printcolumn:name="ProviderID",type="string",JSONPath=".spec.providerID",description="Provider ID",priority=1
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp",description="Time duration since creation of VerdaMachine"

// VerdaMachine is the Schema for the verdamachines API.
type VerdaMachine struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard object's metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty,omitzero"`

	// spec is the desired state of VerdaMachine.
	// +optional
	Spec VerdaMachineSpec `json:"spec,omitempty,omitzero"`

	// status is the observed state of VerdaMachine.
	// +optional
	Status VerdaMachineStatus `json:"status,omitempty,omitzero"`
}

func (m *VerdaMachine) GetConditions() []metav1.Condition {
	return m.Status.Conditions
}

func (m *VerdaMachine) SetConditions(conditions []metav1.Condition) {
	m.Status.Conditions = conditions
}

// +kubebuilder:object:root=true

// VerdaMachineList contains a list of VerdaMachine.
type VerdaMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VerdaMachine `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(scheme *runtime.Scheme) error {
		scheme.AddKnownTypes(SchemeGroupVersion, &VerdaMachine{}, &VerdaMachineList{})
		return nil
	})
}
