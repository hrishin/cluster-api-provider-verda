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

// Tests for instance state tracking, remediation and no-capacity handling.

package controller

import (
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

func provisionedMachine(nsPrefix string) (*infrav1.VerdaMachine, string) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: nsPrefix}}
	Expect(k8sClient.Create(ctx, ns)).To(Succeed())

	verdaCluster := &infrav1.VerdaCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: ns.Name},
		Spec:       infrav1.VerdaClusterSpec{Location: "FIN-03"},
	}
	Expect(k8sClient.Create(ctx, verdaCluster)).To(Succeed())
	cluster := &clusterv1.Cluster{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: ns.Name},
		Spec: clusterv1.ClusterSpec{InfrastructureRef: clusterv1.ContractVersionedObjectReference{
			APIGroup: infrav1.GroupVersion.Group, Kind: "VerdaCluster", Name: "test",
		}},
	}
	Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
	setOwner(verdaCluster, cluster, "Cluster", clusterv1.GroupVersion.String())
	markClusterProvisioned(cluster, verdaCluster)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "m-bootstrap", Namespace: ns.Name},
		Data:       map[string][]byte{"value": []byte(bootstrapData)},
	}
	Expect(k8sClient.Create(ctx, secret)).To(Succeed())
	vm := &infrav1.VerdaMachine{
		ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: ns.Name, Labels: map[string]string{clusterv1.ClusterNameLabel: "test"}},
		Spec:       infrav1.VerdaMachineSpec{InstanceType: "CPU.4V.16G", Image: "24.04.base"},
	}
	Expect(k8sClient.Create(ctx, vm)).To(Succeed())
	machine := &clusterv1.Machine{
		ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: ns.Name, Labels: map[string]string{clusterv1.ClusterNameLabel: "test"}},
		Spec: clusterv1.MachineSpec{
			ClusterName: "test",
			Bootstrap:   clusterv1.Bootstrap{DataSecretName: ptr.To(secret.Name)},
			InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group, Kind: "VerdaMachine", Name: "m",
			},
		},
	}
	Expect(k8sClient.Create(ctx, machine)).To(Succeed())
	setOwner(vm, machine, "Machine", clusterv1.GroupVersion.String())

	var instanceID string
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(vm), vm)).To(Succeed())
		g.Expect(vm.Status.InstanceID).NotTo(BeEmpty())
		instanceID = vm.Status.InstanceID
	}, timeout, interval).Should(Succeed())
	fakeCloud.SetStatus(instanceID, "running", "203.0.113.50")
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(vm), vm)).To(Succeed())
		g.Expect(ptr.Deref(vm.Status.Initialization.Provisioned, false)).To(BeTrue())
	}, timeout, interval).Should(Succeed())
	return vm, instanceID
}

var _ = Describe("VerdaMachine health", func() {
	BeforeEach(func() { fakeCloud.Reset() })

	It("reports a discontinued instance as terminated without flipping initialization", func() {
		vm, instanceID := provisionedMachine("health-")

		fakeCloud.SetStatus(instanceID, "discontinued", "")

		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(vm), vm)).To(Succeed())
			vm.Annotations = map[string]string{"test/poke": "1"}
			g.Expect(k8sClient.Update(ctx, vm)).To(Succeed())
		}, timeout, interval).Should(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(vm), vm)).To(Succeed())
			g.Expect(conditions.GetReason(vm, infrav1.InstanceReadyCondition)).To(Equal(InstanceTerminatedReason))
		}, timeout, interval).Should(Succeed())
		Expect(conditions.IsFalse(vm, clusterv1.ReadyCondition)).To(BeTrue())
		Expect(ptr.Deref(vm.Status.Initialization.Provisioned, false)).To(BeTrue(), "initialization is one-way per the contract")
		Expect(vm.Status.InstanceState).To(Equal("discontinued"))
	})

	It("retries a create that hit no capacity", func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "nocap-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		verdaCluster := &infrav1.VerdaCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: ns.Name},
			Spec:       infrav1.VerdaClusterSpec{Location: "FIN-03"},
		}
		Expect(k8sClient.Create(ctx, verdaCluster)).To(Succeed())
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: ns.Name},
			Spec: clusterv1.ClusterSpec{InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group, Kind: "VerdaCluster", Name: "test",
			}},
		}
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
		setOwner(verdaCluster, cluster, "Cluster", clusterv1.GroupVersion.String())
		markClusterProvisioned(cluster, verdaCluster)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "m-bootstrap", Namespace: ns.Name},
			Data:       map[string][]byte{"value": []byte(bootstrapData)},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		vm := &infrav1.VerdaMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: ns.Name, Labels: map[string]string{clusterv1.ClusterNameLabel: "test"}},
			Spec:       infrav1.VerdaMachineSpec{InstanceType: "1H100.80S.30V", Image: "24.04.base"},
		}
		Expect(k8sClient.Create(ctx, vm)).To(Succeed())
		machine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: ns.Name, Labels: map[string]string{clusterv1.ClusterNameLabel: "test"}},
			Spec: clusterv1.MachineSpec{
				ClusterName: "test",
				Bootstrap:   clusterv1.Bootstrap{DataSecretName: ptr.To(secret.Name)},
				InfrastructureRef: clusterv1.ContractVersionedObjectReference{
					APIGroup: infrav1.GroupVersion.Group, Kind: "VerdaMachine", Name: "m",
				},
			},
		}
		Expect(k8sClient.Create(ctx, machine)).To(Succeed())
		setOwner(vm, machine, "Machine", clusterv1.GroupVersion.String())

		var first string
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(vm), vm)).To(Succeed())
			g.Expect(vm.Status.InstanceID).NotTo(BeEmpty())
			first = vm.Status.InstanceID
		}, timeout, interval).Should(Succeed())

		fakeCloud.SetStatus(first, "no_capacity", "")

		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(vm), vm)).To(Succeed())
			g.Expect(vm.Status.InstanceID).NotTo(BeEmpty())
			g.Expect(vm.Status.InstanceID).NotTo(Equal(first))
		}, timeout, interval).Should(Succeed())
		Expect(fakeCloud.Instances[first].Status).To(Equal("discontinued"), "the failed instance is deleted")
		Expect(conditions.GetReason(vm, infrav1.InstanceReadyCondition)).To(Equal(InstanceProvisioningReason))
	})
})

var _ = Describe("VerdaMachine hostname collisions", func() {
	It("does not create an instance when an unmanaged instance has the hostname", func() {
		fakeCloud.Reset()
		fakeCloud.Instances["foreign"] = &cloud.Instance{ID: "foreign", Hostname: "m", Status: "running", Tags: map[string]string{}}
		defer delete(fakeCloud.Instances, "foreign")

		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "collide-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		verdaCluster := &infrav1.VerdaCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: ns.Name},
			Spec:       infrav1.VerdaClusterSpec{Location: "FIN-03"},
		}
		Expect(k8sClient.Create(ctx, verdaCluster)).To(Succeed())
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: ns.Name},
			Spec: clusterv1.ClusterSpec{InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group, Kind: "VerdaCluster", Name: "test",
			}},
		}
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
		setOwner(verdaCluster, cluster, "Cluster", clusterv1.GroupVersion.String())
		markClusterProvisioned(cluster, verdaCluster)
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "m-bootstrap", Namespace: ns.Name},
			Data:       map[string][]byte{"value": []byte(bootstrapData)},
		}
		Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		vm := &infrav1.VerdaMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: ns.Name, Labels: map[string]string{clusterv1.ClusterNameLabel: "test"}},
			Spec:       infrav1.VerdaMachineSpec{InstanceType: "CPU.4V.16G", Image: "24.04.base"},
		}
		Expect(k8sClient.Create(ctx, vm)).To(Succeed())
		machine := &clusterv1.Machine{
			ObjectMeta: metav1.ObjectMeta{Name: "m", Namespace: ns.Name, Labels: map[string]string{clusterv1.ClusterNameLabel: "test"}},
			Spec: clusterv1.MachineSpec{
				ClusterName: "test",
				Bootstrap:   clusterv1.Bootstrap{DataSecretName: ptr.To(secret.Name)},
				InfrastructureRef: clusterv1.ContractVersionedObjectReference{
					APIGroup: infrav1.GroupVersion.Group, Kind: "VerdaMachine", Name: "m",
				},
			},
		}
		Expect(k8sClient.Create(ctx, machine)).To(Succeed())
		setOwner(vm, machine, "Machine", clusterv1.GroupVersion.String())

		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(vm), vm)).To(Succeed())
			g.Expect(conditions.GetReason(vm, infrav1.InstanceReadyCondition)).To(Equal("HostnameInUse"))
		}, timeout, interval).Should(Succeed())
		Expect(vm.Status.InstanceID).To(BeEmpty())
		Expect(fakeCloud.Created).To(BeEmpty())
	})
})

var _ = Describe("Orphaned objects", func() {
	It("lets a VerdaCluster and a VerdaMachine without owners be deleted", func() {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "orphan-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
		vc := &infrav1.VerdaCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: ns.Name},
			Spec:       infrav1.VerdaClusterSpec{Location: "FIN-03", ControlPlaneLoadBalancer: infrav1.ControlPlaneLoadBalancer{Enabled: ptr.To(true)}},
		}
		Expect(k8sClient.Create(ctx, vc)).To(Succeed())
		vm := &infrav1.VerdaMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "orphan", Namespace: ns.Name},
			Spec:       infrav1.VerdaMachineSpec{InstanceType: "CPU.4V.16G", Image: "24.04.base"},
		}
		Expect(k8sClient.Create(ctx, vm)).To(Succeed())

		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(vc), vc)).To(Succeed())
			g.Expect(vc.Finalizers).To(ContainElement(infrav1.ClusterFinalizer))
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(vm), vm)).To(Succeed())
			g.Expect(vm.Finalizers).To(ContainElement(infrav1.MachineFinalizer))
		}, timeout, interval).Should(Succeed())
		Expect(k8sClient.Delete(ctx, vc)).To(Succeed())
		Expect(k8sClient.Delete(ctx, vm)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(vc), &infrav1.VerdaCluster{}))).To(BeTrue())
			g.Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(vm), &infrav1.VerdaMachine{}))).To(BeTrue())
		}, timeout, interval).Should(Succeed())
	})
})
