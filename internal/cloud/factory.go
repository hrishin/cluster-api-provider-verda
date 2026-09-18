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

// Client factories: a static client and one reading credentials from a VerdaCluster's identity Secret.

package cloud

import (
	"context"
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	SecretKeyClientID     = "client-id"
	SecretKeyClientSecret = "client-secret"
	SecretKeyBaseURL      = "base-url"
)

type Identity struct {
	Namespace  string
	SecretName string
}

type Factory interface {
	ClientFor(ctx context.Context, identity Identity) (Client, error)
}

type SecretFactory struct {
	Reader client.Reader
	Global Client

	NewClient func(Credentials) (Client, error)

	mu    sync.Mutex
	cache map[string]cachedClient
}

type cachedClient struct {
	resourceVersion string
	client          Client
}

var _ Factory = &SecretFactory{}

func (f *SecretFactory) ClientFor(ctx context.Context, identity Identity) (Client, error) {
	if identity.SecretName == "" {
		if f.Global == nil {
			return nil, fmt.Errorf("no identityRef set and the manager has no global Verda credentials")
		}
		return f.Global, nil
	}

	secret := &corev1.Secret{}
	key := client.ObjectKey{Namespace: identity.Namespace, Name: identity.SecretName}
	if err := f.Reader.Get(ctx, key, secret); err != nil {
		return nil, fmt.Errorf("getting identity secret %s: %w", key, err)
	}
	creds := Credentials{
		ClientID:     string(secret.Data[SecretKeyClientID]),
		ClientSecret: string(secret.Data[SecretKeyClientSecret]),
		BaseURL:      string(secret.Data[SecretKeyBaseURL]),
	}
	if creds.ClientID == "" || creds.ClientSecret == "" {
		return nil, fmt.Errorf("identity secret %s must have %s and %s", key, SecretKeyClientID, SecretKeyClientSecret)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cache == nil {
		f.cache = map[string]cachedClient{}
	}
	cacheKey := key.String()
	if c, ok := f.cache[cacheKey]; ok && c.resourceVersion == secret.ResourceVersion {
		return c.client, nil
	}
	newClient := f.NewClient
	if newClient == nil {
		newClient = NewClient
	}
	c, err := newClient(creds)
	if err != nil {
		return nil, err
	}
	f.cache[cacheKey] = cachedClient{resourceVersion: secret.ResourceVersion, client: c}
	return c, nil
}

type StaticFactory struct{ Client Client }

var _ Factory = StaticFactory{}

func (s StaticFactory) ClientFor(context.Context, Identity) (Client, error) { return s.Client, nil }
