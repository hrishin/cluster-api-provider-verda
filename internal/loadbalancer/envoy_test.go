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

// Tests for the envoy configuration rendering.

package loadbalancer

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

var sampleFrontends = []Frontend{
	{Name: "default_nginx_80_tcp", Protocol: "TCP", Port: 80, Backends: []Backend{{"10.0.0.2", 31080}, {"10.0.0.1", 31080}}, ProxyProtocol: true},
	{Name: "default_dns_53_udp", Protocol: "UDP", Port: 53, Backends: []Backend{{"10.0.0.1", 31053}}},
	{Name: "default_local_8080_tcp", Protocol: "TCP", Port: 8080, Backends: []Backend{{"10.0.0.1", 31808}}, HealthCheckPort: 32000},
}

func TestEnvoyResources(t *testing.T) {
	lds, cds := EnvoyResources(sampleFrontends)
	for _, doc := range []string{lds, cds, EnvoyBootstrap()} {
		var v map[string]any
		if err := yaml.Unmarshal([]byte(doc), &v); err != nil {
			t.Fatalf("not valid YAML: %v\n%s", err, doc)
		}
	}
	for _, want := range []string{
		"name: default_dns_53_udp",
		"socket_address: {protocol: UDP, address: 0.0.0.0, port_value: 53}",
		"envoy.filters.udp_listener.udp_proxy",
		"socket_address: {address: 0.0.0.0, port_value: 80}",
		"envoy.filters.network.tcp_proxy",
	} {
		if !strings.Contains(lds, want) {
			t.Errorf("lds missing %q\n%s", want, lds)
		}
	}
	for _, want := range []string{
		"socket_address: {address: 10.0.0.1, port_value: 31080}",
		"socket_address: {address: 10.0.0.2, port_value: 31080}",
		"tcp_health_check: {}",
		"alt_port: 32000",
		"http_health_check: {path: /healthz}",
		"upstream_proxy_protocol",
	} {
		if !strings.Contains(cds, want) {
			t.Errorf("cds missing %q\n%s", want, cds)
		}
	}
	if strings.Index(cds, "10.0.0.1, port_value: 31080") > strings.Index(cds, "10.0.0.2, port_value: 31080") {
		t.Error("backends should be sorted for stable output")
	}

	udpCluster := cds[strings.Index(cds, "name: default_dns_53_udp"):strings.Index(cds, "name: default_local_8080_tcp")]
	if strings.Contains(udpCluster, "health_checks") || strings.Contains(udpCluster, "proxy_protocol") {
		t.Errorf("udp cluster should have no health check or proxy protocol:\n%s", udpCluster)
	}

	lds, cds = EnvoyResources(nil)
	if !strings.Contains(lds, "resources:") || !strings.Contains(cds, "resources:") {
		t.Error("empty resources should still render")
	}
}

func TestEnvoyStartupScriptAndInstallScript(t *testing.T) {
	keys, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	script := EnvoyStartupScript(keys)
	for _, want := range []string{"#!/bin/bash", "envoy-" + EnvoyVersion + "-linux-x86_64", "/etc/systemd/system/envoy.service", "--mode validate", EnvoyLDSPath, EnvoyCDSPath} {
		if !strings.Contains(script, want) {
			t.Errorf("startup script missing %q", want)
		}
	}

	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	files := []File{{Path: filepath.Join(root, "cds.yaml"), Content: "a: 1\n"}, {Path: filepath.Join(root, "lds.yaml"), Content: "b: 2\n"}}
	install := InstallScript(files, "touch "+filepath.Join(root, "reloaded"))
	cmd := exec.Command("bash", "-s")
	cmd.Stdin = strings.NewReader(install)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install script failed: %v\n%s", err, out)
	}
	for _, f := range files {
		got, err := os.ReadFile(f.Path)
		if err != nil || string(got) != f.Content {
			t.Errorf("%s = %q, err=%v", f.Path, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "reloaded")); err != nil {
		t.Error("reload command did not run")
	}
	if entries, _ := os.ReadDir(root); len(entries) != 3 {
		t.Errorf("temporary files left behind: %v", entries)
	}
}
