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

var _ = Describe("VerdaCluster with a managed control plane load balancer", func() {
	var (
		ns           *corev1.Namespace
		cluster      *clusterv1.Cluster
		verdaCluster *infrav1.VerdaCluster
	)

	// controlPlaneVerdaMachine creates a control-plane-labelled VerdaMachine
	// that already reports the given external IP, as the machine controller
	// would once its instance is running.
	controlPlaneVerdaMachine := func(name, ip string) *infrav1.VerdaMachine {
		vm := &infrav1.VerdaMachine{
			ObjectMeta: metav1.ObjectMeta{
				Name: name, Namespace: ns.Name,
				Labels: map[string]string{
					clusterv1.ClusterNameLabel:         cluster.Name,
					clusterv1.MachineControlPlaneLabel: "",
				},
			},
			Spec: infrav1.VerdaMachineSpec{InstanceType: "CPU.4V.16G", Image: "img"},
		}
		Expect(k8sClient.Create(ctx, vm)).To(Succeed())
		setExternalIP(vm, ip)
		return vm
	}

	BeforeEach(func() {
		fakeCloud.Reset()
		fakeLB.Backends = nil
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "lb-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		verdaCluster = &infrav1.VerdaCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: ns.Name},
			Spec: infrav1.VerdaClusterSpec{
				Location:                 "FIN-03",
				ControlPlaneLoadBalancer: infrav1.ControlPlaneLoadBalancer{Enabled: ptr.To(true)},
			},
		}
		Expect(k8sClient.Create(ctx, verdaCluster)).To(Succeed())
		cluster = &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "ha", Namespace: ns.Name},
			Spec: clusterv1.ClusterSpec{
				InfrastructureRef: clusterv1.ContractVersionedObjectReference{
					APIGroup: infrav1.GroupVersion.Group, Kind: "VerdaCluster", Name: verdaCluster.Name,
				},
			},
		}
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
		setOwner(verdaCluster, cluster, "Cluster", clusterv1.GroupVersion.String())
	})

	It("creates the load balancer, publishes its address and syncs backends", func() {
		By("creating SSH keys and the load balancer instance")
		var lbID string
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(verdaCluster.Status.LoadBalancer.InstanceID).NotTo(BeEmpty())
			lbID = verdaCluster.Status.LoadBalancer.InstanceID
		}, timeout, interval).Should(Succeed())

		secret := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: "ha-lb-ssh"}, secret)).To(Succeed())
		Expect(secret.Data).To(HaveKey("ssh-privatekey"))
		Expect(secret.Data).To(HaveKey("host-publickey"))
		Expect(metav1.IsControlledBy(secret, verdaCluster)).To(BeTrue())

		Expect(fakeCloud.Created).To(HaveLen(1))
		spec := fakeCloud.Created[0]
		Expect(spec.Hostname).To(Equal("ha-lb"))
		Expect(spec.InstanceType).To(Equal(infrav1.DefaultLoadBalancerInstanceType))
		Expect(spec.Image).To(Equal(infrav1.DefaultLoadBalancerImage))
		Expect(spec.Tags).To(HaveKeyWithValue(cloud.TagLoadBalancer, ns.Name+"/ha"))
		Expect(spec.StartupScript).To(ContainSubstring("haproxy"))
		Expect(spec.StartupScript).To(ContainSubstring("ssh_host_ed25519_key"))
		Expect(ptr.Deref(verdaCluster.Status.Initialization.Provisioned, false)).To(BeFalse(), "not provisioned until the LB has an address")
		Expect(conditions.GetReason(verdaCluster, infrav1.LoadBalancerReadyCondition)).To(Equal("InstanceProvisioning"))

		By("publishing the address once the instance runs")
		fakeCloud.SetStatus(lbID, "running", "198.51.100.10")
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(ptr.Deref(verdaCluster.Status.Initialization.Provisioned, false)).To(BeTrue())
		}, timeout, interval).Should(Succeed())
		Expect(verdaCluster.Spec.ControlPlaneEndpoint).To(Equal(clusterv1.APIEndpoint{Host: "198.51.100.10", Port: 6443}))
		Expect(verdaCluster.Status.LoadBalancer.Address).To(Equal("198.51.100.10"))
		Expect(conditions.IsTrue(verdaCluster, infrav1.LoadBalancerReadyCondition)).To(BeTrue())
		Expect(conditions.IsTrue(verdaCluster, clusterv1.ReadyCondition)).To(BeTrue())
		Expect(fakeLB.Backends["198.51.100.10"]).To(BeEmpty(), "initial sync with no control plane machines")

		By("adding control plane machines to the backends as they get addresses")
		cp0 := controlPlaneVerdaMachine("ha-cp-0", "203.0.113.1")
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(verdaCluster.Status.LoadBalancer.Backends).To(Equal([]string{"203.0.113.1"}))
		}, timeout, interval).Should(Succeed())
		controlPlaneVerdaMachine("ha-cp-1", "203.0.113.2")
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(verdaCluster.Status.LoadBalancer.Backends).To(Equal([]string{"203.0.113.1", "203.0.113.2"}))
		}, timeout, interval).Should(Succeed())
		Expect(fakeLB.Backends["198.51.100.10"]).To(Equal([]string{"203.0.113.1", "203.0.113.2"}))

		By("ignoring workers")
		worker := &infrav1.VerdaMachine{
			ObjectMeta: metav1.ObjectMeta{Name: "ha-w-0", Namespace: ns.Name, Labels: map[string]string{clusterv1.ClusterNameLabel: cluster.Name}},
			Spec:       infrav1.VerdaMachineSpec{InstanceType: "CPU.4V.16G", Image: "img"},
		}
		Expect(k8sClient.Create(ctx, worker)).To(Succeed())
		setExternalIP(worker, "203.0.113.99")
		Consistently(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(verdaCluster.Status.LoadBalancer.Backends).To(Equal([]string{"203.0.113.1", "203.0.113.2"}))
		}, 2*interval*4, interval).Should(Succeed())

		By("removing a control plane machine that is being deleted")
		// Give it a finalizer so the object lingers with a deletionTimestamp,
		// as it would while its instance is torn down.
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cp0), cp0)).To(Succeed())
			cp0.Finalizers = append(cp0.Finalizers, "test.verda/hold")
			g.Expect(k8sClient.Update(ctx, cp0)).To(Succeed())
		}, timeout, interval).Should(Succeed())
		Expect(k8sClient.Delete(ctx, cp0)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(verdaCluster.Status.LoadBalancer.Backends).To(Equal([]string{"203.0.113.2"}))
		}, timeout, interval).Should(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cp0), cp0)).To(Succeed())
			cp0.Finalizers = nil
			g.Expect(k8sClient.Update(ctx, cp0)).To(Succeed())
		}, timeout, interval).Should(Succeed())

		By("deleting the load balancer with the cluster")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
		lbScript := verdaCluster.Status.LoadBalancer.StartupScriptID
		Expect(lbScript).NotTo(BeEmpty())
		Expect(k8sClient.Delete(ctx, verdaCluster)).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster))
		}, timeout, interval).Should(BeTrue())
		Expect(fakeCloud.Instances[lbID].Status).To(Equal("discontinued"))
		Expect(fakeCloud.Scripts).NotTo(HaveKey(lbScript))
	})

	It("keeps the cluster provisioned while the first backend sync retries", func() {
		fakeLB.Err = errTestSSH
		defer func() { fakeLB.Err = nil }()
		controlPlaneVerdaMachine("ha-cp-0", "203.0.113.1")

		var lbID string
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(verdaCluster.Status.LoadBalancer.InstanceID).NotTo(BeEmpty())
			lbID = verdaCluster.Status.LoadBalancer.InstanceID
		}, timeout, interval).Should(Succeed())
		fakeCloud.SetStatus(lbID, "running", "198.51.100.11")

		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(ptr.Deref(verdaCluster.Status.Initialization.Provisioned, false)).To(BeTrue())
			g.Expect(conditions.GetReason(verdaCluster, infrav1.LoadBalancerReadyCondition)).To(Equal("BackendUpdateFailed"))
		}, timeout, interval).Should(Succeed())
		Expect(verdaCluster.Spec.ControlPlaneEndpoint.Host).To(Equal("198.51.100.11"))
		Expect(conditions.IsFalse(verdaCluster, clusterv1.ReadyCondition)).To(BeTrue())
	})
})

// setExternalIP records an external IP on a VerdaMachine's status, retrying
// on conflicts with the machine controller.
func setExternalIP(vm *infrav1.VerdaMachine, ip string) {
	Eventually(func(g Gomega) {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(vm), vm)).To(Succeed())
		vm.Status.Addresses = []clusterv1.MachineAddress{{Type: clusterv1.MachineExternalIP, Address: ip}}
		g.Expect(k8sClient.Status().Update(ctx, vm)).To(Succeed())
	}, timeout, interval).Should(Succeed())
}

var errTestSSH = fmtError("ssh: connection refused")

type fmtError string

func (e fmtError) Error() string { return string(e) }
