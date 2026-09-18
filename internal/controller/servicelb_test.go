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

// Tests for the envoy service load balancer.

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
	"sigs.k8s.io/cluster-api/util/secret"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
	"github.com/hrishin/verda-capi/internal/cloud"
)

var _ = Describe("VerdaCluster with a service load balancer", func() {
	It("provisions envoy and publishes its address and keys to the workload cluster", func() {
		fakeCloud.Reset()
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "svclb-"}}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())

		verdaCluster := &infrav1.VerdaCluster{
			ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: ns.Name},
			Spec: infrav1.VerdaClusterSpec{
				Location:             "FIN-03",
				ControlPlaneEndpoint: clusterv1.APIEndpoint{Host: "k8s.example.com", Port: 6443},
				ServiceLoadBalancer:  infrav1.ControlPlaneLoadBalancer{Enabled: ptr.To(true), InstanceType: "CPU.8V.32G"},
			},
		}
		Expect(k8sClient.Create(ctx, verdaCluster)).To(Succeed())
		cluster := &clusterv1.Cluster{
			ObjectMeta: metav1.ObjectMeta{Name: "svc", Namespace: ns.Name},
			Spec: clusterv1.ClusterSpec{InfrastructureRef: clusterv1.ContractVersionedObjectReference{
				APIGroup: infrav1.GroupVersion.Group, Kind: "VerdaCluster", Name: "svc",
			}},
		}
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
		setOwner(verdaCluster, cluster, "Cluster", clusterv1.GroupVersion.String())

		By("creating the instance from the envoy startup script")
		var lbID string
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(verdaCluster.Status.ServiceLoadBalancer.InstanceID).NotTo(BeEmpty())
			lbID = verdaCluster.Status.ServiceLoadBalancer.InstanceID
		}, timeout, interval).Should(Succeed())
		Expect(ptr.Deref(verdaCluster.Status.Initialization.Provisioned, false)).To(BeTrue(), "the service LB never blocks cluster provisioning")
		spec := fakeCloud.Created[len(fakeCloud.Created)-1]
		Expect(spec.Hostname).To(Equal("svc-service-lb"))
		Expect(spec.InstanceType).To(Equal("CPU.8V.32G"))
		Expect(spec.Tags).To(HaveKeyWithValue(cloud.TagServiceLoadBalancer, ns.Name+"/svc"))
		Expect(spec.StartupScript).To(ContainSubstring("/usr/local/bin/envoy"))
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: "svc-service-lb-ssh"}, &corev1.Secret{})).To(Succeed())

		By("waiting for the workload cluster kubeconfig once the instance runs")
		fakeCloud.SetStatus(lbID, "running", "198.51.100.20")
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(conditions.GetReason(verdaCluster, infrav1.ServiceLoadBalancerReadyCondition)).To(Equal("WaitingForWorkloadCluster"))
		}, timeout, interval).Should(Succeed())
		Expect(verdaCluster.Status.ServiceLoadBalancer.Address).To(Equal("198.51.100.20"))

		By("publishing the secret into the workload cluster")

		kubeconfigSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: secret.Name(cluster.Name, secret.Kubeconfig), Namespace: ns.Name,
				Labels: map[string]string{clusterv1.ClusterNameLabel: cluster.Name}},
			Data: map[string][]byte{secret.KubeconfigDataName: []byte("apiVersion: v1\nkind: Config\n")},
		}
		Expect(k8sClient.Create(ctx, kubeconfigSecret)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(conditions.IsTrue(verdaCluster, infrav1.ServiceLoadBalancerReadyCondition)).To(BeTrue())
		}, timeout, interval).Should(Succeed())
		published := &corev1.Secret{}
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: infrav1.ServiceLoadBalancerSecretName}, published)).To(Succeed())
		Expect(string(published.Data["address"])).To(Equal("198.51.100.20"))
		Expect(published.Data).To(HaveKey("ssh-privatekey"))
		Expect(published.Data).To(HaveKey("host-publickey"))

		By("tearing the instance down when the load balancer is disabled")
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			verdaCluster.Spec.ServiceLoadBalancer.Enabled = ptr.To(false)
			g.Expect(k8sClient.Update(ctx, verdaCluster)).To(Succeed())
		}, timeout, interval).Should(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(verdaCluster.Status.ServiceLoadBalancer.InstanceID).To(BeEmpty())
			g.Expect(conditions.Has(verdaCluster, infrav1.ServiceLoadBalancerReadyCondition)).To(BeFalse())
		}, timeout, interval).Should(Succeed())
		Expect(fakeCloud.Instances[lbID].Status).To(Equal("discontinued"))
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKey{Namespace: "kube-system", Name: infrav1.ServiceLoadBalancerSecretName}, &corev1.Secret{}))).To(BeTrue(), "workload secret removed")
		Expect(ptr.Deref(verdaCluster.Status.Initialization.Provisioned, false)).To(BeTrue())

		By("re-enabling creates a fresh instance")
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			verdaCluster.Spec.ServiceLoadBalancer.Enabled = ptr.To(true)
			g.Expect(k8sClient.Update(ctx, verdaCluster)).To(Succeed())
		}, timeout, interval).Should(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster)).To(Succeed())
			g.Expect(verdaCluster.Status.ServiceLoadBalancer.InstanceID).NotTo(BeEmpty())
			g.Expect(verdaCluster.Status.ServiceLoadBalancer.InstanceID).NotTo(Equal(lbID))
			lbID = verdaCluster.Status.ServiceLoadBalancer.InstanceID
		}, timeout, interval).Should(Succeed())

		By("deleting the instance with the cluster")
		Expect(k8sClient.Delete(ctx, verdaCluster)).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(verdaCluster), verdaCluster))
		}, timeout, interval).Should(BeTrue())
		Expect(fakeCloud.Instances[lbID].Status).To(Equal("discontinued"))
	})
})
