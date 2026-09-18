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

// cloudprovider.Interface and InstancesV2 for Verda, keyed by verda://<hostname> provider IDs.

package ccm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"

	"github.com/hrishin/verda-capi/internal/cloud"
	"github.com/hrishin/verda-capi/internal/loadbalancer"
)

const ProviderName = "verda"

const providerIDPrefix = ProviderName + "://"

func init() {
	cloudprovider.RegisterCloudProvider(ProviderName, func(io.Reader) (cloudprovider.Interface, error) {
		creds, err := cloud.LoadCredentials("")
		if err != nil {
			return nil, err
		}
		client, err := cloud.NewClient(creds)
		if err != nil {
			return nil, err
		}
		return New(client), nil
	})
}

type Provider struct {
	client cloud.Client
	kube   kubernetes.Interface
	lb     *serviceLB
}

var (
	_ cloudprovider.Interface   = &Provider{}
	_ cloudprovider.InstancesV2 = &Provider{}
)

func New(client cloud.Client) *Provider {
	return &Provider{client: client}
}

func (p *Provider) Initialize(builder cloudprovider.ControllerClientBuilder, _ <-chan struct{}) {
	p.WithKubeClient(builder.ClientOrDie("verda-service-load-balancer"), &loadbalancer.SSHUpdater{})
}

func (p *Provider) WithKubeClient(kube kubernetes.Interface, updater loadbalancer.Updater) *Provider {
	p.kube = kube
	p.lb = &serviceLB{kube: kube, updater: updater}
	return p
}

func (p *Provider) Instances() (cloudprovider.Instances, bool) { return nil, false }

func (p *Provider) InstancesV2() (cloudprovider.InstancesV2, bool) { return p, true }

func (p *Provider) Zones() (cloudprovider.Zones, bool) { return nil, false }

func (p *Provider) Clusters() (cloudprovider.Clusters, bool) { return nil, false }

func (p *Provider) Routes() (cloudprovider.Routes, bool) { return nil, false }

func (p *Provider) ProviderName() string { return ProviderName }

func (p *Provider) HasClusterID() bool { return true }

func (p *Provider) InstanceExists(ctx context.Context, node *corev1.Node) (bool, error) {
	instance, err := p.lookup(ctx, node)
	if err != nil {
		if errors.Is(err, cloud.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	switch instance.Status {
	case cloud.StatusDiscontinued, cloud.StatusDeleted, cloud.StatusNotFound:
		return false, nil
	}
	return true, nil
}

func (p *Provider) InstanceShutdown(ctx context.Context, node *corev1.Node) (bool, error) {
	instance, err := p.lookup(ctx, node)
	if err != nil {
		return false, err
	}
	return instance.Status == cloud.StatusOffline, nil
}

func (p *Provider) InstanceMetadata(ctx context.Context, node *corev1.Node) (*cloudprovider.InstanceMetadata, error) {
	instance, err := p.lookup(ctx, node)
	if err != nil {
		return nil, err
	}
	addresses := []corev1.NodeAddress{{Type: corev1.NodeHostName, Address: instance.Hostname}}
	if instance.IP != "" {

		addresses = append(addresses,
			corev1.NodeAddress{Type: corev1.NodeExternalIP, Address: instance.IP},
			corev1.NodeAddress{Type: corev1.NodeInternalIP, Address: instance.IP},
		)
	}
	return &cloudprovider.InstanceMetadata{
		ProviderID:    providerIDPrefix + instance.Hostname,
		InstanceType:  instance.InstanceType,
		NodeAddresses: addresses,
		Zone:          instance.Location,
		Region:        instance.Location,
	}, nil
}

func (p *Provider) lookup(ctx context.Context, node *corev1.Node) (*cloud.Instance, error) {
	hostname := node.Name
	if id := node.Spec.ProviderID; id != "" {
		if !strings.HasPrefix(id, providerIDPrefix) {
			return nil, fmt.Errorf("node %s has foreign provider ID %q", node.Name, id)
		}
		hostname = strings.TrimPrefix(id, providerIDPrefix)
	}
	instance, err := p.client.FindInstanceByHostname(ctx, hostname)
	if err != nil {
		if errors.Is(err, cloud.ErrNotFound) {
			klog.V(4).InfoS("No Verda instance for node", "hostname", hostname, "node", node.Name)
		}
		return nil, err
	}
	return instance, nil
}
