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

// Tests for the defaulting and validation webhooks.

package v1beta1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
)

var _ = Describe("VerdaMachine webhook", func() {
	It("validates and defaults machines", func() {
		vm := &infrav1.VerdaMachine{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "vm-", Namespace: "default"},
			Spec:       infrav1.VerdaMachineSpec{InstanceType: "CPU.4V.16G"},
		}

		Expect(k8sClient.Create(ctx, vm)).To(MatchError(ContainSubstring("image or osVolumeID")))

		vm.Spec.Image = "ubuntu-24.04"
		vm.Spec.OSVolumeID = "vol"
		Expect(k8sClient.Create(ctx, vm)).To(MatchError(ContainSubstring("image or osVolumeID")))

		vm.Spec.OSVolumeID = ""
		vm.Spec.Spot = ptr.To(true)
		Expect(k8sClient.Create(ctx, vm)).To(Succeed())
		Expect(vm.Spec.Contract).To(Equal("SPOT"), "spot defaults the contract")

		By("rejecting changes to immutable fields")
		vm.Spec.InstanceType = "CPU.8V.32G"
		Expect(k8sClient.Update(ctx, vm)).To(MatchError(ContainSubstring("instanceType is immutable")))

		By("allowing providerID to be set once but not changed")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(vm), vm)).To(Succeed())
		vm.Spec.ProviderID = "verda://a"
		Expect(k8sClient.Update(ctx, vm)).To(Succeed())
		vm.Spec.ProviderID = "verda://b"
		Expect(k8sClient.Update(ctx, vm)).To(MatchError(ContainSubstring("providerID cannot be changed")))
	})
})

var _ = Describe("VerdaMachineTemplate webhook", func() {
	It("keeps the template immutable except for topology dry runs", func() {
		tpl := &infrav1.VerdaMachineTemplate{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "vmt-", Namespace: "default"},
			Spec: infrav1.VerdaMachineTemplateSpec{Template: infrav1.VerdaMachineTemplateResource{
				Spec: infrav1.VerdaMachineSpec{InstanceType: "CPU.4V.16G", Image: "ubuntu-24.04"},
			}},
		}
		Expect(k8sClient.Create(ctx, tpl)).To(Succeed())

		tpl.Spec.Template.Spec.Image = "ubuntu-22.04"
		Expect(k8sClient.Update(ctx, tpl)).To(MatchError(ContainSubstring("spec is immutable")))

		By("allowing a dry-run update from the topology controller")
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tpl), tpl)).To(Succeed())
		tpl.Annotations = map[string]string{clusterv1.TopologyDryRunAnnotation: ""}
		tpl.Spec.Template.Spec.Image = "ubuntu-22.04"
		Expect(k8sClient.Update(ctx, tpl, client.DryRunAll)).To(Succeed())

		By("still rejecting a real update carrying the annotation")
		Expect(k8sClient.Update(ctx, tpl)).To(MatchError(ContainSubstring("spec is immutable")))
	})
})

var _ = Describe("VerdaCluster webhook", func() {
	It("validates and defaults clusters", func() {
		vc := &infrav1.VerdaCluster{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "vc-", Namespace: "default"},
			Spec:       infrav1.VerdaClusterSpec{Location: "helsinki"},
		}
		Expect(k8sClient.Create(ctx, vc)).To(MatchError(ContainSubstring("must be a Verda location code")))

		vc.Spec.Location = "FIN-03"
		vc.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "k8s.example.com"}
		Expect(k8sClient.Create(ctx, vc)).To(Succeed())
		Expect(vc.Spec.ControlPlaneEndpoint.Port).To(Equal(int32(6443)), "port defaults")

		vc.Spec.ControlPlaneEndpoint.Host = "other.example.com"
		Expect(k8sClient.Update(ctx, vc)).To(MatchError(ContainSubstring("controlPlaneEndpoint is immutable once set")))

		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(vc), vc)).To(Succeed())
		vc.Spec.Location = "FIN-01"
		Expect(k8sClient.Update(ctx, vc)).To(MatchError(ContainSubstring("location is immutable")))

		By("defaulting the load balancer")
		lb := &infrav1.VerdaCluster{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "vc-lb-", Namespace: "default"},
			Spec: infrav1.VerdaClusterSpec{
				Location:                 "FIN-03",
				ControlPlaneLoadBalancer: infrav1.ControlPlaneLoadBalancer{Enabled: ptr.To(true)},
			},
		}
		Expect(k8sClient.Create(ctx, lb)).To(Succeed())
		Expect(lb.Spec.ControlPlaneLoadBalancer.InstanceType).To(Equal(infrav1.DefaultLoadBalancerInstanceType))
		Expect(lb.Spec.ControlPlaneLoadBalancer.Image).To(Equal(infrav1.DefaultLoadBalancerImage))

		By("allowing the controller to set the endpoint once, then freezing it")
		lb.Spec.ControlPlaneEndpoint = clusterv1.APIEndpoint{Host: "198.51.100.1", Port: 6443}
		Expect(k8sClient.Update(ctx, lb)).To(Succeed())
		lb.Spec.ControlPlaneLoadBalancer.Enabled = ptr.To(false)
		Expect(k8sClient.Update(ctx, lb)).To(MatchError(ContainSubstring("cannot be enabled or disabled after creation")))
	})
})
