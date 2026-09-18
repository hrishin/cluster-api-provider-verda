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

// haproxy configuration, SSH key generation and startup script for the control plane load balancer instance.

package loadbalancer

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"slices"

	"golang.org/x/crypto/ssh"
)

const Port = 6443

const ConfigPath = "/etc/haproxy/haproxy.cfg"

func Config(backends []string) string {
	sorted := slices.Clone(backends)
	slices.Sort(sorted)
	return render("haproxy.cfg.tmpl", map[string]any{"Backends": sorted, "Port": Port})
}

type Keys struct {
	ClientPrivateKey []byte
	ClientPublicKey  []byte
	HostPrivateKey   []byte
	HostPublicKey    []byte
}

func GenerateKeys() (*Keys, error) {
	clientPriv, clientPub, err := generateKeyPair("cluster-api-provider-verda")
	if err != nil {
		return nil, fmt.Errorf("generating client key: %w", err)
	}
	hostPriv, hostPub, err := generateKeyPair("")
	if err != nil {
		return nil, fmt.Errorf("generating host key: %w", err)
	}
	return &Keys{ClientPrivateKey: clientPriv, ClientPublicKey: clientPub, HostPrivateKey: hostPriv, HostPublicKey: hostPub}, nil
}

func generateKeyPair(comment string) (privPEM, pubLine []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, comment)
	if err != nil {
		return nil, nil, err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(block), ssh.MarshalAuthorizedKey(sshPub), nil
}

func StartupScript(keys *Keys, backends []string) string {
	return render("haproxy-startup.sh.tmpl", map[string]string{
		"ClientPublicKey": b64(keys.ClientPublicKey),
		"HostPrivateKey":  b64(keys.HostPrivateKey),
		"HostPublicKey":   b64(keys.HostPublicKey),
		"ConfigPath":      ConfigPath,
		"Config":          b64([]byte(Config(backends))),
	})
}

const UpdateCommand = "set -e; cat > " + ConfigPath + ".new && haproxy -c -f " + ConfigPath + ".new >/dev/null && mv " + ConfigPath + ".new " + ConfigPath + " && systemctl reload haproxy"
