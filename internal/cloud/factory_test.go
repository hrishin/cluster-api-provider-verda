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

// Tests for the Secret-backed client factory.

package cloud

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSecretFactory(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "ns", ResourceVersion: "1"},
		Data:       map[string][]byte{SecretKeyClientID: []byte("id"), SecretKeyClientSecret: []byte("secret")},
	}
	k8s := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	var built []Credentials
	global := NewFake()
	f := &SecretFactory{
		Reader: k8s,
		Global: global,
		NewClient: func(c Credentials) (Client, error) {
			built = append(built, c)
			return NewFake(), nil
		},
	}
	ctx := context.Background()

	if c, err := f.ClientFor(ctx, Identity{Namespace: "ns"}); err != nil || c != global {
		t.Fatalf("no identity should return the global client: %v %v", c, err)
	}

	c1, err := f.ClientFor(ctx, Identity{Namespace: "ns", SecretName: "creds"})
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := f.ClientFor(ctx, Identity{Namespace: "ns", SecretName: "creds"})
	if c1 != c2 || len(built) != 1 {
		t.Errorf("client should be cached per secret version; built %d times", len(built))
	}
	if built[0].ClientID != "id" || built[0].ClientSecret != "secret" {
		t.Errorf("credentials not read from secret: %+v", built[0])
	}

	secret.ResourceVersion = ""
	secret.Data[SecretKeyClientSecret] = []byte("rotated")
	if err := k8s.Update(ctx, secret); err != nil {
		t.Fatal(err)
	}
	c3, _ := f.ClientFor(ctx, Identity{Namespace: "ns", SecretName: "creds"})
	if c3 == c1 || len(built) != 2 || built[1].ClientSecret != "rotated" {
		t.Errorf("rotated secret should rebuild the client: built=%d", len(built))
	}

	if _, err := f.ClientFor(ctx, Identity{Namespace: "ns", SecretName: "missing"}); err == nil {
		t.Error("missing secret should error")
	}
	if _, err := (&SecretFactory{Reader: k8s}).ClientFor(ctx, Identity{}); err == nil {
		t.Error("no identity and no global client should error")
	}
}
