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

// Tests for the VerdaMachineTemplate capacity reconciler.

package controller

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/cluster-api/util/conditions"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
)

var _ = Describe("VerdaMachineTemplate capacity", func() {
	It("fills capacity and nodeInfo from the instance type catalog", func() {
		tpl := &infrav1.VerdaMachineTemplate{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "gpu-", Namespace: "default"},
			Spec: infrav1.VerdaMachineTemplateSpec{Template: infrav1.VerdaMachineTemplateResource{
				Spec: infrav1.VerdaMachineSpec{InstanceType: "1H100.80S.30V", Image: "ubuntu-24.04"},
			}},
		}
		Expect(k8sClient.Create(ctx, tpl)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tpl), tpl)).To(Succeed())
			g.Expect(conditions.IsTrue(tpl, infrav1.CapacityReadyCondition)).To(BeTrue())
		}, timeout, interval).Should(Succeed())
		Expect(tpl.Status.Capacity.Cpu().Value()).To(Equal(int64(30)))
		Expect(tpl.Status.Capacity.Memory().Value()).To(Equal(int64(120) << 30))
		gpus := tpl.Status.Capacity[corev1.ResourceName("nvidia.com/gpu")]
		Expect(gpus.Value()).To(Equal(int64(1)))
		Expect(tpl.Status.NodeInfo).To(Equal(infrav1.NodeInfo{Architecture: "amd64", OperatingSystem: "linux"}))

		By("reporting an unknown instance type")
		unknown := &infrav1.VerdaMachineTemplate{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "unknown-", Namespace: "default"},
			Spec: infrav1.VerdaMachineTemplateSpec{Template: infrav1.VerdaMachineTemplateResource{
				Spec: infrav1.VerdaMachineSpec{InstanceType: "NOPE.1", Image: "ubuntu-24.04"},
			}},
		}
		Expect(k8sClient.Create(ctx, unknown)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(unknown), unknown)).To(Succeed())
			g.Expect(conditions.GetReason(unknown, infrav1.CapacityReadyCondition)).To(Equal("UnknownInstanceType"))
		}, timeout, interval).Should(Succeed())
		Expect(unknown.Status.Capacity).To(BeEmpty())
	})
})
