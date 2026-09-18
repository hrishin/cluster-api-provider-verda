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

// Tests for the haproxy configuration rendering.

package loadbalancer

import (
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestConfig(t *testing.T) {
	cfg := Config([]string{"10.0.0.2", "10.0.0.1"})
	for _, want := range []string{
		"bind *:6443",
		"server cp-0 10.0.0.1:6443 check",
		"server cp-1 10.0.0.2:6443 check",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("config missing %q:\n%s", want, cfg)
		}
	}
	if Config(nil) == "" || strings.Contains(Config(nil), "server cp-") {
		t.Errorf("empty backend list should render a backend without servers:\n%s", Config(nil))
	}
	if Config([]string{"a", "b"}) != Config([]string{"b", "a"}) {
		t.Error("config should be independent of backend order")
	}
}

func TestGenerateKeysAndStartupScript(t *testing.T) {
	keys, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.ParsePrivateKey(keys.ClientPrivateKey); err != nil {
		t.Errorf("client private key does not parse: %v", err)
	}
	if _, err := ssh.ParsePrivateKey(keys.HostPrivateKey); err != nil {
		t.Errorf("host private key does not parse: %v", err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(keys.ClientPublicKey)
	if err != nil || pub.Type() != ssh.KeyAlgoED25519 {
		t.Errorf("client public key: type=%v err=%v", pub, err)
	}

	script := StartupScript(keys, []string{"10.0.0.1"})
	for _, want := range []string{"#!/bin/bash", "apt-get install -y haproxy", "/root/.ssh/authorized_keys", "/etc/ssh/ssh_host_ed25519_key", ConfigPath, "systemctl restart haproxy"} {
		if !strings.Contains(script, want) {
			t.Errorf("startup script missing %q", want)
		}
	}
	if strings.Contains(script, string(keys.HostPrivateKey)) {
		t.Error("private key should be base64-wrapped, not embedded raw")
	}
}
