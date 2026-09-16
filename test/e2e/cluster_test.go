//go:build e2e

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

package e2e

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
	"github.com/hrishin/verda-capi/internal/cloud"
)

const (
	provisionTimeout = 25 * time.Minute
	deleteTimeout    = 15 * time.Minute
)

var _ = Describe("Cluster lifecycle", Ordered, func() {
	clusterName := fmt.Sprintf("e2e-%d", time.Now().Unix()%100000)
	namespace := "default"
	var manifest string

	BeforeAll(func() {
		By("rendering the default template with clusterctl")
		out, err := exec.Command("clusterctl", "generate", "cluster", clusterName,
			"--from", filepath.Join("..", "..", "templates", "cluster-template.yaml"),
			"--kubernetes-version", envOr("E2E_KUBERNETES_VERSION", "v1.35.8"),
			"--control-plane-machine-count", "1",
			"--worker-machine-count", "1",
		).CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), string(out))
		manifest = filepath.Join(GinkgoT().TempDir(), "cluster.yaml")
		Expect(os.WriteFile(manifest, out, 0o600)).To(Succeed())
	})

	It("creates a cluster behind the managed load balancer", func() {
		Expect(kubectl("apply", "-f", manifest)).To(Succeed())

		By("waiting for the load balancer and control plane endpoint")
		verdaCluster := &infrav1.VerdaCluster{}
		Eventually(func(g Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: clusterName}, verdaCluster)).To(Succeed())
			g.Expect(ptr.Deref(verdaCluster.Status.Initialization.Provisioned, false)).To(BeTrue())
		}, 10*time.Minute).Should(Succeed())
		Expect(verdaCluster.Spec.ControlPlaneEndpoint.Host).To(Equal(verdaCluster.Status.LoadBalancer.Address))

		By("waiting for all machines to run")
		Eventually(func(g Gomega) {
			g.Expect(runningMachines(g, namespace, clusterName)).To(Equal(2))
		}, provisionTimeout).Should(Succeed())

		By("checking the control plane is a load balancer backend")
		Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: clusterName}, verdaCluster)).To(Succeed())
		Expect(verdaCluster.Status.LoadBalancer.Backends).To(HaveLen(1))
	})

	It("scales the workers up and down", func() {
		md := &clusterv1.MachineDeployment{}
		mdKey := client.ObjectKey{Namespace: namespace, Name: clusterName + "-md-0"}
		Expect(k8sClient.Get(ctx, mdKey, md)).To(Succeed())
		md.Spec.Replicas = ptr.To(int32(2))
		Expect(k8sClient.Update(ctx, md)).To(Succeed())
		Eventually(func(g Gomega) {
			g.Expect(runningMachines(g, namespace, clusterName)).To(Equal(3))
		}, provisionTimeout).Should(Succeed())

		Expect(k8sClient.Get(ctx, mdKey, md)).To(Succeed())
		md.Spec.Replicas = ptr.To(int32(1))
		Expect(k8sClient.Update(ctx, md)).To(Succeed())
		Eventually(func(g Gomega) {
			machines := &clusterv1.MachineList{}
			g.Expect(k8sClient.List(ctx, machines, client.InNamespace(namespace), client.MatchingLabels{clusterv1.ClusterNameLabel: clusterName})).To(Succeed())
			g.Expect(machines.Items).To(HaveLen(2))
		}, deleteTimeout).Should(Succeed())
	})

	It("deletes the cluster and leaves nothing on Verda", func() {
		Expect(kubectl("delete", "cluster", "-n", namespace, clusterName, "--wait=false")).To(Succeed())
		Eventually(func() bool {
			return apierrors.IsNotFound(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: clusterName}, &clusterv1.Cluster{}))
		}, deleteTimeout).Should(BeTrue())

		_, err := verdaClient.FindInstanceByTag(ctx, cloud.TagCluster, namespace+"/"+clusterName)
		Expect(errors.Is(err, cloud.ErrNotFound)).To(BeTrue(), "instances tagged for the cluster still exist")
		_, err = verdaClient.FindInstanceByTag(ctx, cloud.TagLoadBalancer, namespace+"/"+clusterName)
		Expect(errors.Is(err, cloud.ErrNotFound)).To(BeTrue(), "the load balancer instance still exists")
	})

	AfterAll(func() {
		// Best-effort cleanup if an earlier step failed.
		_ = kubectl("delete", "cluster", "-n", namespace, clusterName, "--ignore-not-found", "--wait=false")
	})
})

func runningMachines(g Gomega, namespace, clusterName string) int {
	machines := &clusterv1.MachineList{}
	g.Expect(k8sClient.List(ctx, machines, client.InNamespace(namespace), client.MatchingLabels{clusterv1.ClusterNameLabel: clusterName})).To(Succeed())
	running := 0
	for _, m := range machines.Items {
		if m.Status.Phase == string(clusterv1.MachinePhaseRunning) {
			running++
		}
	}
	return running
}

func kubectl(args ...string) error {
	cmd := exec.Command("kubectl", args...)
	cmd.Stdout = GinkgoWriter
	cmd.Stderr = GinkgoWriter
	return cmd.Run()
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
