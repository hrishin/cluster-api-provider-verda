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

package controller

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"
	"sigs.k8s.io/cluster-api/util/conditions"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
	"github.com/hrishin/verda-capi/internal/cloud"
)

const (
	timeout  = 20 * time.Second
	interval = 250 * time.Millisecond
)

const bootstrapData = `## template: jinja
#cloud-config
write_files:
-   path: /run/kubeadm/kubeadm.yaml
    content: |
      kind: ClusterConfiguration
runcmd:
  - kubeadm init --config /run/kubeadm/kubeadm.yaml
`

// Machine and cluster lifecycle, driven the way core Cluster API drives an
// infrastructure provider: owner references, cluster label, bootstrap secret.
var _ = Describe("VerdaCluster and VerdaMachine lifecycle", func() {
	var (
		ns           *corev1.Namespace
		cluster      *clusterv1.Cluster
		verdaCluster *infrav1.VerdaCluster
	)

	BeforeEach(func() {
		fakeCloud.Reset()
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "lifecycle-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		verdaCluster = &infrav1.VerdaCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: ns.Name},
			Spec:       infrav1.VerdaClusterSpec{Location: "FIN-01"},
		}
		Expect(k8sClient.Create(ctx, verdaCluster)).To(Succeed())

		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: ns.Name},
			Spec: clusterv1.ClusterSpec{
				InfrastructureRef: clusterv1.ContractVersionedObjectReference{
					APIGroup: infrav1.GroupVersion.Group,
					Kind:     "VerdaCluster",
					Name:     verdaCluster.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())

		// The core Cluster controller is not running in envtest; set the owner
		// reference it would set.
		setOwner(verdaCluster, cluster, "Cluster", clusterv1.GroupVersion.String())
	})

	It("does not provision the cluster until a control plane endpoint is set", func() {
		Consistently(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(ptr.Deref(verdaCluster.Status.Initialization.Provisioned, false)).To(BeFalse())
		}, 2*time.Second, interval).Should(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(verdaCluster.Finalizers).To(ContainElement(infrav1.ClusterFinalizer))
			g.Expect(conditions.IsFalse(verdaCluster, infrav1.ControlPlaneEndpointReadyCondition)).To(BeTrue())
			g.Expect(conditions.IsFalse(verdaCluster, clusterv1.ReadyCondition)).To(BeTrue())
		}, timeout, interval).Should(Succeed())
	})

	It("provisions the cluster once an endpoint is set and defaults the port", func() {
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			verdaCluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "k8s.example.com"}
			g.Expect(k8sClient.Update(ctx, verdaCluster)).To(Succeed())
		}, timeout, interval).Should(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(ptr.Deref(verdaCluster.Status.Initialization.Provisioned, false)).To(BeTrue())
			g.Expect(verdaCluster.Spec.ControlPlaneEndpoint.Port).To(Equal(int32(6443)))
			g.Expect(conditions.IsTrue(verdaCluster, clusterv1.ReadyCondition)).To(BeTrue())
			g.Expect(conditions.IsFalse(verdaCluster, clusterv1.PausedCondition)).To(BeTrue())
		}, timeout, interval).Should(Succeed())
		Expect(verdaCluster.Status.FailureDomains).To(HaveLen(1))
		Expect(verdaCluster.Status.FailureDomains[0].Name).To(Equal("FIN-01"))
		Expect(*verdaCluster.Status.FailureDomains[0].ControlPlane).To(BeTrue())
	})

	Context("with a provisioned cluster", func() {
		var (
			machine      *clusterv1.Machine
			verdaMachine *infrav1.VerdaMachine
		)

		BeforeEach(func() {
			markClusterProvisioned(cluster, verdaCluster)

			verdaMachine = &infrav1.VerdaMachine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cp-0",
					Namespace: ns.Name,
					Labels:    map[string]string{clusterv1.ClusterNameLabel: cluster.Name},
				},
				Spec: infrav1.VerdaMachineSpec{
					InstanceType: "CPU.4V.16G",
					Image:        "ubuntu-24.04",
					SSHKeyIDs:    []string{"key-1"},
				},
			}
			Expect(k8sClient.Create(ctx, verdaMachine)).To(Succeed())

			machine = &clusterv1.Machine{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "cp-0",
					Namespace: ns.Name,
					Labels: map[string]string{
						clusterv1.ClusterNameLabel:         cluster.Name,
						clusterv1.MachineControlPlaneLabel: "",
					},
				},
				Spec: clusterv1.MachineSpec{
					ClusterName: cluster.Name,
					Bootstrap: clusterv1.Bootstrap{
						ConfigRef: clusterv1.ContractVersionedObjectReference{
							APIGroup: "bootstrap.cluster.x-k8s.io",
							Kind:     "KubeadmConfig",
							Name:     "cp-0",
						},
					},
					InfrastructureRef: clusterv1.ContractVersionedObjectReference{
						APIGroup: infrav1.GroupVersion.Group,
						Kind:     "VerdaMachine",
						Name:     verdaMachine.Name,
					},
				},
			}
			Expect(k8sClient.Create(ctx, machine)).To(Succeed())
			setOwner(verdaMachine, machine, "Machine", clusterv1.GroupVersion.String())
		})

		It("waits for bootstrap data before creating an instance", func() {
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaMachine), verdaMachine)).To(Succeed())
				g.Expect(conditions.GetReason(verdaMachine, infrav1.InstanceReadyCondition)).To(Equal("WaitingForBootstrapData"))
			}, timeout, interval).Should(Succeed())
			Expect(fakeCloud.Created).To(BeEmpty())
		})

		It("creates, provisions and deletes the instance", func() {
			By("providing bootstrap data")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "cp-0-bootstrap", Namespace: ns.Name},
				Data:       map[string][]byte{"value": []byte(bootstrapData)},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(machine), machine)).To(Succeed())
				machine.Spec.Bootstrap.DataSecretName = ptr.To(secret.Name)
				g.Expect(k8sClient.Update(ctx, machine)).To(Succeed())
			}, timeout, interval).Should(Succeed())

			By("creating the instance")
			var instanceID string
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaMachine), verdaMachine)).To(Succeed())
				g.Expect(verdaMachine.Status.InstanceID).NotTo(BeEmpty())
				instanceID = verdaMachine.Status.InstanceID
			}, timeout, interval).Should(Succeed())

			Expect(fakeCloud.Created).To(HaveLen(1))
			spec := fakeCloud.Created[0]
			Expect(spec.Hostname).To(Equal("cp-0"))
			Expect(spec.Location).To(Equal("FIN-01"))
			Expect(spec.InstanceType).To(Equal("CPU.4V.16G"))
			Expect(spec.SSHKeyIDs).To(Equal([]string{"key-1"}))
			Expect(spec.Tags).To(HaveKeyWithValue(cloud.TagMachine, ns.Name+"/cp-0"))
			Expect(spec.Tags).To(HaveKeyWithValue("capi-role", "control-plane"))
			Expect(spec.StartupScript).To(HavePrefix("#!/bin/bash"))
			Expect(spec.StartupScript).To(ContainSubstring("kubeadm init"))

			Expect(verdaMachine.Spec.ProviderID).To(Equal("verda://cp-0"))
			Expect(ptr.Deref(verdaMachine.Status.Initialization.Provisioned, false)).To(BeFalse())
			Expect(conditions.GetReason(verdaMachine, infrav1.InstanceReadyCondition)).To(Equal("InstanceProvisioning"))

			By("observing the instance become running")
			fakeCloud.SetStatus(instanceID, "running", "203.0.113.10")
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaMachine), verdaMachine)).To(Succeed())
				g.Expect(ptr.Deref(verdaMachine.Status.Initialization.Provisioned, false)).To(BeTrue())
			}, timeout, interval).Should(Succeed())
			Expect(conditions.IsTrue(verdaMachine, clusterv1.ReadyCondition)).To(BeTrue())
			Expect(verdaMachine.Status.Addresses).To(ContainElement(clusterv1.MachineAddress{Type: clusterv1.MachineExternalIP, Address: "203.0.113.10"}))
			Expect(verdaMachine.Status.FailureDomain).To(Equal("FIN-01"))

			By("deleting the VerdaMachine")
			scriptID := verdaMachine.Status.StartupScriptID
			Expect(scriptID).NotTo(BeEmpty())
			Expect(k8sClient.Delete(ctx, verdaMachine)).To(Succeed())
			Eventually(func() bool {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaMachine), verdaMachine)
				return apierrors.IsNotFound(err)
			}, timeout, interval).Should(BeTrue(), "VerdaMachine should be gone once the instance is deleted")
			Expect(fakeCloud.Instances[instanceID].Status).To(Equal("discontinued"))
			Expect(fakeCloud.Scripts).NotTo(HaveKey(scriptID), "startup script should be cleaned up")
		})
	})
})

var _ = Describe("VerdaMachine booting from an OS volume", func() {
	var (
		ns           *corev1.Namespace
		cluster      *clusterv1.Cluster
		verdaCluster *infrav1.VerdaCluster
		machine      *clusterv1.Machine
		verdaMachine *infrav1.VerdaMachine
	)

	BeforeEach(func() {
		fakeCloud.Reset()
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "osvolume-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		verdaCluster = &infrav1.VerdaCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: ns.Name},
			Spec:       infrav1.VerdaClusterSpec{Location: "FIN-03"},
		}
		Expect(k8sClient.Create(ctx, verdaCluster)).To(Succeed())
		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: ns.Name},
			Spec: clusterv1.ClusterSpec{
				InfrastructureRef: clusterv1.ContractVersionedObjectReference{
					APIGroup: infrav1.GroupVersion.Group, Kind: "VerdaCluster", Name: verdaCluster.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
		setOwner(verdaCluster, cluster, "Cluster", clusterv1.GroupVersion.String())
		markClusterProvisioned(cluster, verdaCluster)

		// The golden image volume, not created by us and therefore untagged.
		fakeCloud.Volumes["golden"] = &cloud.Volume{ID: "golden", Name: "k8s-node", Status: "detached", Location: "FIN-03", IsOSVolume: true}

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "w-0-bootstrap", Namespace: ns.Name},
			Data:       map[string][]byte{"value": []byte(bootstrapData)},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())

		verdaMachine = &infrav1.VerdaMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name: "w-0", Namespace: ns.Name,
				Labels: map[string]string{clusterv1.ClusterNameLabel: cluster.Name},
			},
			Spec: infrav1.VerdaMachineSpec{
				InstanceType: "CPU.4V.16G",
				// Reference the golden volume by its name, not its ID.
				OSVolumeID:        "k8s-node",
				AdditionalVolumes: []infrav1.AdditionalVolume{{Name: "data", SizeGB: 100}},
			},
		}
		Expect(k8sClient.Create(ctx, verdaMachine)).To(Succeed())
		machine = &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{
				Name: "w-0", Namespace: ns.Name,
				Labels: map[string]string{clusterv1.ClusterNameLabel: cluster.Name},
			},
			Spec: clusterv1.MachineSpec{
				ClusterName: cluster.Name,
				Bootstrap:   clusterv1.Bootstrap{DataSecretName: ptr.To(secret.Name)},
				InfrastructureRef: clusterv1.ContractVersionedObjectReference{
					APIGroup: infrav1.GroupVersion.Group, Kind: "VerdaMachine", Name: verdaMachine.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, machine)).To(Succeed())
		setOwner(verdaMachine, machine, "Machine", clusterv1.GroupVersion.String())
	})

	It("rejects a spec with both image and osVolumeID", func() {
		bad := &infrav1.VerdaMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "bad", Namespace: ns.Name},
			Spec:       infrav1.VerdaMachineSpec{InstanceType: "x", Image: "a", OSVolumeID: "b"},
		}
		Expect(k8sClient.Create(ctx, bad)).To(MatchError(ContainSubstring("exactly one of image or osVolumeID")))
	})

	It("clones the volume, waits for it, boots from the clone and cleans up", func() {
		By("cloning the golden volume")
		var cloneID string
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaMachine), verdaMachine)).To(Succeed())
			g.Expect(verdaMachine.Status.OSVolumeID).NotTo(BeEmpty())
			cloneID = verdaMachine.Status.OSVolumeID
		}, timeout, interval).Should(Succeed())
		Expect(cloneID).NotTo(Equal("golden"))
		clone, err := fakeCloud.GetVolume(ctx, cloneID)
		Expect(err).NotTo(HaveOccurred())
		Expect(clone.Name).To(Equal(ns.Name + "-w-0-os"))
		Expect(clone.Location).To(Equal("FIN-03"))
		Expect(conditions.GetReason(verdaMachine, infrav1.InstanceReadyCondition)).To(Equal("WaitingForOSVolume"))
		Expect(verdaMachine.Status.InstanceID).To(BeEmpty(), "no instance until the clone is ready")

		By("booting from the clone once it is detached")
		fakeCloud.SetVolumeStatus(cloneID, "detached")
		var instanceID string
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaMachine), verdaMachine)).To(Succeed())
			g.Expect(verdaMachine.Status.InstanceID).NotTo(BeEmpty())
			instanceID = verdaMachine.Status.InstanceID
		}, timeout, interval).Should(Succeed())
		Expect(conditions.IsTrue(verdaMachine, infrav1.OSVolumeReadyCondition)).To(BeTrue())
		created := fakeCloud.Created[len(fakeCloud.Created)-1]
		Expect(created.Image).To(Equal(cloneID))
		Expect(created.OSVolumeSizeGB).To(BeZero())
		Expect(created.DataVolumes).To(Equal([]cloud.DataVolumeSpec{{Name: "data", SizeGB: 100}}))

		By("deleting: instance, clone and script are removed; the golden volume is untouched")
		fakeCloud.SetStatus(instanceID, "running", "203.0.113.20")
		Expect(k8sClient.Delete(ctx, verdaMachine)).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaMachine), verdaMachine))
		}, timeout, interval).Should(BeTrue())
		Expect(fakeCloud.Instances[instanceID].Status).To(Equal("discontinued"))
		Expect(fakeCloud.Volumes).NotTo(HaveKey(cloneID))
		Expect(fakeCloud.Volumes).To(HaveKey("golden"))
	})
})

// setOwner mimics the owner reference that core Cluster API sets on
// infrastructure objects.
func setOwner(obj client.Object, owner client.Object, kind, apiVersion string) {
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(Succeed())
		obj.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion: apiVersion,
			Kind:       kind,
			Name:       owner.GetName(),
			UID:        owner.GetUID(),
		}})
		g.Expect(k8sClient.Update(ctx, obj)).To(Succeed())
	}, timeout, interval).Should(Succeed())
}

// markClusterProvisioned sets the endpoint on the VerdaCluster and mirrors what
// the core Cluster controller would write into Cluster.status.
func markClusterProvisioned(cluster *clusterv1.Cluster, verdaCluster *infrav1.VerdaCluster) {
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
		verdaCluster.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "k8s.example.com", Port: 6443}
		g.Expect(k8sClient.Update(ctx, verdaCluster)).To(Succeed())
	}, timeout, interval).Should(Succeed())
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
		g.Expect(ptr.Deref(verdaCluster.Status.Initialization.Provisioned, false)).To(BeTrue())
	}, timeout, interval).Should(Succeed())

	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cluster), cluster)).To(Succeed())
		cluster.Status.Initialization.InfrastructureProvisioned = ptr.To(true)
		g.Expect(k8sClient.Status().Update(ctx, cluster)).To(Succeed())
	}, timeout, interval).Should(Succeed(), fmt.Sprintf("marking cluster %s provisioned", cluster.Name))
}
