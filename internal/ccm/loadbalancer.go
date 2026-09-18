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

// cloudprovider.LoadBalancer for Verda: renders envoy listeners on the service load balancer instance for every Service of type LoadBalancer.

package ccm

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"

	"github.com/hrishin/verda-capi/internal/loadbalancer"
)

const (
	AnnotationProxyProtocol = "verda.cluster.x-k8s.io/proxy-protocol"
)

const serviceLBSecretName = "verda-service-lb"

const excludeFromLBLabel = "node.kubernetes.io/exclude-from-external-load-balancers"

type serviceLB struct {
	kube    kubernetes.Interface
	updater loadbalancer.Updater

	mu sync.Mutex
}

func (p *Provider) LoadBalancer() (cloudprovider.LoadBalancer, bool) {
	if p.kube == nil {
		return nil, false
	}
	return p.lb, true
}

func (l *serviceLB) GetLoadBalancerName(_ context.Context, _ string, service *corev1.Service) string {
	return service.Namespace + "_" + service.Name
}

func (l *serviceLB) GetLoadBalancer(ctx context.Context, _ string, service *corev1.Service) (*corev1.LoadBalancerStatus, bool, error) {
	lb, err := l.config(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if len(service.Status.LoadBalancer.Ingress) == 0 {
		return nil, false, nil
	}
	return status(lb.address), true, nil
}

func (l *serviceLB) EnsureLoadBalancer(ctx context.Context, _ string, service *corev1.Service, _ []*corev1.Node) (*corev1.LoadBalancerStatus, error) {
	lb, err := l.config(ctx)
	if err != nil {
		return nil, err
	}
	if err := l.sync(ctx, lb, service); err != nil {
		return nil, err
	}
	return status(lb.address), nil
}

func (l *serviceLB) UpdateLoadBalancer(ctx context.Context, _ string, service *corev1.Service, _ []*corev1.Node) error {
	lb, err := l.config(ctx)
	if err != nil {
		return err
	}
	return l.sync(ctx, lb, service)
}

func (l *serviceLB) EnsureLoadBalancerDeleted(ctx context.Context, _ string, service *corev1.Service) error {
	lb, err := l.config(ctx)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return l.sync(ctx, lb, nil, service)
}

type lbConfig struct {
	address string
	keys    *loadbalancer.Keys
}

func (l *serviceLB) config(ctx context.Context) (*lbConfig, error) {
	secret, err := l.kube.CoreV1().Secrets(metav1.NamespaceSystem).Get(ctx, serviceLBSecretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, apierrors.NewNotFound(corev1.Resource("secrets"), serviceLBSecretName+
				" (enable spec.serviceLoadBalancer on the VerdaCluster to get a load balancer)")
		}
		return nil, fmt.Errorf("reading %s/%s: %w", metav1.NamespaceSystem, serviceLBSecretName, err)
	}
	address := string(secret.Data["address"])
	if address == "" || len(secret.Data["ssh-privatekey"]) == 0 || len(secret.Data["host-publickey"]) == 0 {
		return nil, fmt.Errorf("secret %s/%s is incomplete", metav1.NamespaceSystem, serviceLBSecretName)
	}
	return &lbConfig{
		address: address,
		keys:    &loadbalancer.Keys{ClientPrivateKey: secret.Data["ssh-privatekey"], HostPublicKey: secret.Data["host-publickey"]},
	}, nil
}

func (l *serviceLB) sync(ctx context.Context, lb *lbConfig, current *corev1.Service, deleting ...*corev1.Service) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	services, err := l.kube.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing services: %w", err)
	}
	nodes, err := l.backendNodes(ctx)
	if err != nil {
		return err
	}

	skip := map[string]bool{}
	for _, d := range deleting {
		skip[d.Namespace+"/"+d.Name] = true
	}
	var all []*corev1.Service
	for i := range services.Items {
		svc := &services.Items[i]
		if svc.Spec.Type != corev1.ServiceTypeLoadBalancer || skip[svc.Namespace+"/"+svc.Name] || !svc.DeletionTimestamp.IsZero() {
			continue
		}
		if current != nil && svc.Namespace == current.Namespace && svc.Name == current.Name {
			continue
		}
		all = append(all, svc)
	}
	if current != nil {
		all = append(all, current)
	}

	frontends, conflicts := Frontends(all, nodes)
	if current != nil {
		if reason, ok := conflicts[current.Namespace+"/"+current.Name]; ok {
			return fmt.Errorf("service %s/%s: %s", current.Namespace, current.Name, reason)
		}
	}
	for key, reason := range conflicts {
		klog.InfoS("Service not exposed on the load balancer", "service", key, "reason", reason)
	}

	if err := l.updater.UpdateFiles(ctx, lb.address, lb.keys, loadbalancer.EnvoyFiles(frontends), ""); err != nil {
		return err
	}
	klog.V(2).InfoS("Updated service load balancer", "address", lb.address, "listeners", len(frontends), "backends", len(nodes))
	return nil
}

func (l *serviceLB) backendNodes(ctx context.Context) ([]string, error) {
	nodes, err := l.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing nodes: %w", err)
	}
	var out []string
	for i := range nodes.Items {
		node := &nodes.Items[i]
		if _, excluded := node.Labels[excludeFromLBLabel]; excluded {
			continue
		}
		if !node.DeletionTimestamp.IsZero() {
			continue
		}
		if addr := nodeAddress(node); addr != "" {
			out = append(out, addr)
		}
	}
	slices.Sort(out)
	return out, nil
}

func Frontends(services []*corev1.Service, nodes []string) ([]loadbalancer.Frontend, map[string]string) {
	sorted := slices.Clone(services)
	slices.SortFunc(sorted, func(a, b *corev1.Service) int {
		if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
			return c
		}
		return cmp.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name)
	})

	conflicts := map[string]string{}
	taken := map[string]string{}
	var frontends []loadbalancer.Frontend
	for _, svc := range sorted {
		key := svc.Namespace + "/" + svc.Name
		var mine []loadbalancer.Frontend
		for _, port := range svc.Spec.Ports {
			proto := string(port.Protocol)
			if proto == "" {
				proto = "TCP"
			}
			if proto != "TCP" && proto != "UDP" {
				conflicts[key] = fmt.Sprintf("protocol %s is not supported", proto)
				break
			}
			if port.NodePort == 0 {
				conflicts[key] = fmt.Sprintf("port %d has no nodePort yet", port.Port)
				break
			}
			slot := fmt.Sprintf("%d/%s", port.Port, proto)
			if owner, ok := taken[slot]; ok {
				conflicts[key] = fmt.Sprintf("port %s is already used by %s on the shared load balancer", slot, owner)
				break
			}
			f := loadbalancer.Frontend{
				Name:          frontendName(svc, port),
				Protocol:      proto,
				Port:          port.Port,
				ProxyProtocol: strings.EqualFold(svc.Annotations[AnnotationProxyProtocol], "true"),
			}
			if svc.Spec.ExternalTrafficPolicy == corev1.ServiceExternalTrafficPolicyLocal && svc.Spec.HealthCheckNodePort != 0 {
				f.HealthCheckPort = svc.Spec.HealthCheckNodePort
			}
			for _, n := range nodes {
				f.Backends = append(f.Backends, loadbalancer.Backend{Address: n, Port: port.NodePort})
			}
			mine = append(mine, f)
		}
		if _, conflicted := conflicts[key]; conflicted {
			continue
		}
		for _, f := range mine {
			taken[fmt.Sprintf("%d/%s", f.Port, f.Protocol)] = key
		}
		frontends = append(frontends, mine...)
	}
	return frontends, conflicts
}

func frontendName(svc *corev1.Service, port corev1.ServicePort) string {
	proto := strings.ToLower(string(port.Protocol))
	if proto == "" {
		proto = "tcp"
	}
	return sanitize(svc.Namespace) + "_" + sanitize(svc.Name) + "_" + fmt.Sprint(port.Port) + "_" + proto
}

func sanitize(s string) string {
	return strings.NewReplacer("-", "_", ".", "_").Replace(s)
}

func status(address string) *corev1.LoadBalancerStatus {
	return &corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: address}}}
}

func nodeAddress(node *corev1.Node) string {
	for _, t := range []corev1.NodeAddressType{corev1.NodeInternalIP, corev1.NodeExternalIP} {
		for _, a := range node.Status.Addresses {
			if a.Type == t && a.Address != "" {
				return a.Address
			}
		}
	}
	return ""
}
