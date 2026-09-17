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

package ccm

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/hrishin/verda-capi/internal/cloud"
	"github.com/hrishin/verda-capi/internal/loadbalancer"
)

func lbSecret() *corev1.Secret {
	keys, _ := loadbalancer.GenerateKeys()
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: serviceLBSecretName, Namespace: "kube-system"},
		Data:       map[string][]byte{"address": []byte("198.51.100.5"), "ssh-privatekey": keys.ClientPrivateKey, "host-publickey": keys.HostPublicKey},
	}
}

func node(name, ip string, ready bool, labels map[string]string) *corev1.Node {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels},
		Status: corev1.NodeStatus{
			Addresses:  []corev1.NodeAddress{{Type: corev1.NodeInternalIP, Address: ip}},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}},
		},
	}
}

func lbService(ns, name string, created time.Time, ports ...corev1.ServicePort) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(created)},
		Spec:       corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, Ports: ports},
	}
}

func TestServiceLoadBalancer(t *testing.T) {
	ctx := context.Background()
	t0 := time.Now()
	nginx := lbService("default", "nginx", t0, corev1.ServicePort{Port: 80, NodePort: 31080, Protocol: corev1.ProtocolTCP})
	nginx.Annotations = map[string]string{AnnotationProxyProtocol: "true"}
	dns := lbService("kube-system", "dns", t0.Add(time.Second), corev1.ServicePort{Port: 53, NodePort: 31053, Protocol: corev1.ProtocolUDP})
	later := lbService("default", "later", t0.Add(2*time.Second), corev1.ServicePort{Port: 80, NodePort: 31999, Protocol: corev1.ProtocolTCP})
	clusterIP := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "internal", Namespace: "default"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, Ports: []corev1.ServicePort{{Port: 80}}}}

	kube := fake.NewClientset(lbSecret(), nginx, dns, later, clusterIP,
		node("w-0", "10.0.0.1", true, nil),
		node("w-1", "10.0.0.2", true, nil),
		node("w-2", "10.0.0.3", false, nil), // not Ready yet: still a backend, health checks decide
		node("cp-0", "10.0.0.9", true, map[string]string{excludeFromLBLabel: ""}),
	)
	updater := &loadbalancer.FakeUpdater{}
	p := New(cloud.NewFake()).WithKubeClient(kube, updater)
	lb, ok := p.LoadBalancer()
	if !ok {
		t.Fatal("LoadBalancer should be supported once a kube client is set")
	}

	st, err := lb.EnsureLoadBalancer(ctx, "c", nginx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Ingress[0].IP != "198.51.100.5" {
		t.Errorf("ingress = %+v", st.Ingress)
	}
	files := updater.Files["198.51.100.5"]
	if len(files) != 2 || files[0].Path != loadbalancer.EnvoyCDSPath || files[1].Path != loadbalancer.EnvoyLDSPath {
		t.Fatalf("unexpected files pushed: %+v", files)
	}
	cds, lds := files[0].Content, files[1].Content
	for _, want := range []string{"default_nginx_80_tcp", "kube_system_dns_53_udp", "10.0.0.1, port_value: 31080", "10.0.0.2, port_value: 31080", "10.0.0.3, port_value: 31080", "upstream_proxy_protocol"} {
		if !strings.Contains(cds, want) {
			t.Errorf("cds missing %q", want)
		}
	}
	for _, unwanted := range []string{"10.0.0.9", "default_later_80_tcp", "default_internal"} {
		if strings.Contains(cds, unwanted) || strings.Contains(lds, unwanted) {
			t.Errorf("config should not contain %q (excluded node, conflicting or ClusterIP)", unwanted)
		}
	}

	// The conflicting Service gets an error of its own.
	if _, err := lb.EnsureLoadBalancer(ctx, "c", later, nil); err == nil || !strings.Contains(err.Error(), "already used by default/nginx") {
		t.Errorf("expected port conflict error, got %v", err)
	}

	// Deleting nginx frees the port for later on the next sync.
	if err := lb.EnsureLoadBalancerDeleted(ctx, "c", nginx); err != nil {
		t.Fatal(err)
	}
	if err := kube.CoreV1().Services("default").Delete(ctx, "nginx", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := lb.EnsureLoadBalancer(ctx, "c", later, nil); err != nil {
		t.Fatalf("later should be exposable after nginx is gone: %v", err)
	}
	cds = updater.Files["198.51.100.5"][0].Content
	if !strings.Contains(cds, "default_later_80_tcp") || strings.Contains(cds, "default_nginx_80_tcp") {
		t.Errorf("config after delete: %s", cds)
	}

	// No secret means no load balancer.
	empty := fake.NewClientset()
	p2 := New(cloud.NewFake()).WithKubeClient(empty, &loadbalancer.FakeUpdater{})
	lb2, _ := p2.LoadBalancer()
	if _, err := lb2.EnsureLoadBalancer(ctx, "c", nginx, nil); err == nil || !strings.Contains(err.Error(), "spec.serviceLoadBalancer") {
		t.Errorf("expected a helpful error without the secret, got %v", err)
	}
	if err := lb2.EnsureLoadBalancerDeleted(ctx, "c", nginx); err != nil {
		t.Errorf("deleting without a load balancer should be a no-op: %v", err)
	}
}

func TestFrontendsLocalTrafficPolicy(t *testing.T) {
	svc := lbService("default", "app", time.Now(), corev1.ServicePort{Port: 8080, NodePort: 31808, Protocol: corev1.ProtocolTCP})
	svc.Spec.ExternalTrafficPolicy = corev1.ServiceExternalTrafficPolicyLocal
	svc.Spec.HealthCheckNodePort = 32000
	fs, conflicts := Frontends([]*corev1.Service{svc}, []string{"10.0.0.1"})
	if len(conflicts) != 0 || len(fs) != 1 || fs[0].HealthCheckPort != 32000 {
		t.Errorf("frontends=%+v conflicts=%v", fs, conflicts)
	}
	sctp := lbService("default", "sctp", time.Now(), corev1.ServicePort{Port: 1, NodePort: 31001, Protocol: corev1.ProtocolSCTP})
	if _, conflicts := Frontends([]*corev1.Service{sctp}, nil); conflicts["default/sctp"] == "" {
		t.Error("SCTP should be reported as unsupported")
	}
}
