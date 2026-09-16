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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/hrishin/verda-capi/internal/cloud"
)

func TestProvider(t *testing.T) {
	fake := cloud.NewFake()
	fake.Instances["i-1"] = &cloud.Instance{ID: "i-1", Hostname: "cp-0", Status: "running", IP: "203.0.113.1", Location: "FIN-03", InstanceType: "CPU.4V.16G", Tags: map[string]string{}}
	fake.Instances["i-2"] = &cloud.Instance{ID: "i-2", Hostname: "w-0", Status: "discontinued", Tags: map[string]string{}}
	p := New(fake)
	ctx := context.Background()

	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "cp-0"}}
	meta, err := p.InstanceMetadata(ctx, node)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ProviderID != "verda://cp-0" || meta.Zone != "FIN-03" || meta.InstanceType != "CPU.4V.16G" {
		t.Errorf("unexpected metadata: %+v", meta)
	}
	if len(meta.NodeAddresses) != 3 || meta.NodeAddresses[1].Address != "203.0.113.1" {
		t.Errorf("unexpected addresses: %+v", meta.NodeAddresses)
	}

	// Lookup by provider ID once set.
	node.Spec.ProviderID = "verda://cp-0"
	node.Name = "renamed"
	if ok, err := p.InstanceExists(ctx, node); err != nil || !ok {
		t.Errorf("running instance should exist: %v %v", ok, err)
	}

	gone := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "w-0"}}
	if ok, err := p.InstanceExists(ctx, gone); err != nil || ok {
		t.Errorf("discontinued instance should not exist: %v %v", ok, err)
	}
	missing := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "nope"}}
	if ok, err := p.InstanceExists(ctx, missing); err != nil || ok {
		t.Errorf("missing instance should not exist: %v %v", ok, err)
	}
	foreign := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "x"}, Spec: corev1.NodeSpec{ProviderID: "aws://i-123"}}
	if _, err := p.InstanceExists(ctx, foreign); err == nil {
		t.Error("foreign provider ID should error")
	}
}
