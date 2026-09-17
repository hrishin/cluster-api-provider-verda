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

package loadbalancer

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
)

// Envoy fronts Service type=LoadBalancer traffic. It runs with file-based
// dynamic resources: the cloud controller manager rewrites lds.yaml and
// cds.yaml atomically and envoy applies them without a restart.
const (
	// EnvoyVersion is the envoy release installed on the service load balancer.
	EnvoyVersion = "1.34.1"
	// EnvoyDir holds the bootstrap and the dynamic resource files.
	EnvoyDir = "/etc/envoy"
	// EnvoyLDSPath and EnvoyCDSPath are the dynamic listener and cluster files.
	EnvoyLDSPath = EnvoyDir + "/lds.yaml"
	EnvoyCDSPath = EnvoyDir + "/cds.yaml"
)

// Frontend is one exposed port of a Service and where it is forwarded.
type Frontend struct {
	// Name is unique per load balancer, e.g. default_nginx_80_tcp.
	Name string
	// Protocol is TCP or UDP.
	Protocol string
	// Port is the port envoy listens on.
	Port int32
	// Backends are node address:port pairs the traffic is forwarded to.
	Backends []Backend
	// HealthCheckPort, when set, is an HTTP health check port (the Service's
	// healthCheckNodePort for externalTrafficPolicy=Local) consulted instead
	// of a TCP check on the backend port.
	HealthCheckPort int32
	// ProxyProtocol sends PROXY protocol v2 headers upstream so the backend
	// sees the client address. TCP only.
	ProxyProtocol bool
}

// Backend is one upstream endpoint.
type Backend struct {
	Address string
	Port    int32
}

// EnvoyBootstrap is the static envoy.yaml that points at the dynamic files.
func EnvoyBootstrap() string {
	return fmt.Sprintf(`# Managed by cluster-api-provider-verda. Do not edit.
node:
  id: service-lb
  cluster: service-lb
admin:
  address:
    socket_address: {address: 127.0.0.1, port_value: 9901}
dynamic_resources:
  lds_config:
    path_config_source:
      path: %s
      watched_directory: {path: %s}
  cds_config:
    path_config_source:
      path: %s
      watched_directory: {path: %s}
`, EnvoyLDSPath, EnvoyDir, EnvoyCDSPath, EnvoyDir)
}

// EnvoyResources renders lds.yaml and cds.yaml for the given frontends.
func EnvoyResources(frontends []Frontend) (lds, cds string) {
	sorted := slices.Clone(frontends)
	slices.SortFunc(sorted, func(a, b Frontend) int { return strings.Compare(a.Name, b.Name) })

	var l, c strings.Builder
	l.WriteString("# Managed by cluster-api-provider-verda. Do not edit.\nresources:\n")
	c.WriteString("# Managed by cluster-api-provider-verda. Do not edit.\nresources:\n")
	for _, f := range sorted {
		writeListener(&l, f)
		writeCluster(&c, f)
	}
	return l.String(), c.String()
}

func writeListener(b *strings.Builder, f Frontend) {
	fmt.Fprintf(b, "- \"@type\": type.googleapis.com/envoy.config.listener.v3.Listener\n  name: %s\n", f.Name)
	if strings.EqualFold(f.Protocol, "UDP") {
		fmt.Fprintf(b, "  address:\n    socket_address: {protocol: UDP, address: 0.0.0.0, port_value: %d}\n", f.Port)
		b.WriteString("  listener_filters:\n  - name: envoy.filters.udp_listener.udp_proxy\n    typed_config:\n")
		b.WriteString("      \"@type\": type.googleapis.com/envoy.extensions.filters.udp.udp_proxy.v3.UdpProxyConfig\n")
		fmt.Fprintf(b, "      stat_prefix: %s\n", f.Name)
		b.WriteString("      matcher:\n        on_no_match:\n          action:\n            name: route\n            typed_config:\n")
		b.WriteString("              \"@type\": type.googleapis.com/envoy.extensions.filters.udp.udp_proxy.v3.Route\n")
		fmt.Fprintf(b, "              cluster: %s\n", f.Name)
		return
	}
	fmt.Fprintf(b, "  address:\n    socket_address: {address: 0.0.0.0, port_value: %d}\n", f.Port)
	b.WriteString("  filter_chains:\n  - filters:\n    - name: envoy.filters.network.tcp_proxy\n      typed_config:\n")
	b.WriteString("        \"@type\": type.googleapis.com/envoy.extensions.filters.network.tcp_proxy.v3.TcpProxy\n")
	fmt.Fprintf(b, "        stat_prefix: %s\n        cluster: %s\n", f.Name, f.Name)
}

// healthCheckHeader is the common part of a cluster's active health check.
// no_traffic_interval is lowered from envoy's 60s default so a Service whose
// NodePort was not yet programmed when the first probe ran recovers quickly.
const healthCheckHeader = "  health_checks:\n  - timeout: 2s\n    interval: 5s\n    no_traffic_interval: 5s\n    unhealthy_threshold: 3\n    healthy_threshold: 2\n"

func writeCluster(b *strings.Builder, f Frontend) {
	fmt.Fprintf(b, "- \"@type\": type.googleapis.com/envoy.config.cluster.v3.Cluster\n  name: %s\n", f.Name)
	b.WriteString("  type: STATIC\n  connect_timeout: 5s\n  lb_policy: ROUND_ROBIN\n")
	fmt.Fprintf(b, "  load_assignment:\n    cluster_name: %s\n    endpoints:\n    - lb_endpoints:\n", f.Name)
	backends := slices.Clone(f.Backends)
	slices.SortFunc(backends, func(a, b Backend) int { return strings.Compare(a.Address, b.Address) })
	for _, be := range backends {
		fmt.Fprintf(b, "      - endpoint:\n          address:\n            socket_address: {address: %s, port_value: %d}\n", be.Address, be.Port)
	}
	if len(backends) == 0 {
		// An empty endpoint list is valid YAML for envoy; keep the key.
		b.WriteString("      []\n")
	}
	udp := strings.EqualFold(f.Protocol, "UDP")
	switch {
	case f.HealthCheckPort > 0:
		b.WriteString(healthCheckHeader)
		fmt.Fprintf(b, "    alt_port: %d\n    http_health_check: {path: /healthz}\n", f.HealthCheckPort)
	case !udp:
		b.WriteString(healthCheckHeader)
		b.WriteString("    tcp_health_check: {}\n")
	}
	if f.ProxyProtocol && !udp {
		b.WriteString("  transport_socket:\n    name: envoy.transport_sockets.upstream_proxy_protocol\n    typed_config:\n")
		b.WriteString("      \"@type\": type.googleapis.com/envoy.extensions.transport_sockets.proxy_protocol.v3.ProxyProtocolUpstreamTransport\n")
		b.WriteString("      config: {version: V2}\n      transport_socket:\n        name: envoy.transport_sockets.raw_buffer\n        typed_config:\n")
		b.WriteString("          \"@type\": type.googleapis.com/envoy.extensions.transport_sockets.raw_buffer.v3.RawBuffer\n")
	}
}

// EnvoyStartupScript renders the first-boot script for the service load
// balancer: it installs the envoy release binary, the provider's SSH keys and
// a systemd unit, and starts envoy with empty listener and cluster sets.
func EnvoyStartupScript(keys *Keys) string {
	lds, cds := EnvoyResources(nil)
	var b strings.Builder
	b.WriteString(`#!/bin/bash
# Generated by cluster-api-provider-verda for the service load balancer. Do not edit.
set -euo pipefail
exec > >(tee -a /var/log/capi-loadbalancer.log) 2>&1
echo "[capi-lb] starting $(date -Is)"

# SSH: provider client key and pre-generated host key.
mkdir -p /root/.ssh && chmod 700 /root/.ssh
`)
	fmt.Fprintf(&b, "base64 -d >> /root/.ssh/authorized_keys <<'__CAPI_EOF__'\n%s\n__CAPI_EOF__\n", base64.StdEncoding.EncodeToString(keys.ClientPublicKey))
	b.WriteString("chmod 600 /root/.ssh/authorized_keys\n")
	fmt.Fprintf(&b, "base64 -d > /etc/ssh/ssh_host_ed25519_key <<'__CAPI_EOF__'\n%s\n__CAPI_EOF__\n", base64.StdEncoding.EncodeToString(keys.HostPrivateKey))
	fmt.Fprintf(&b, "base64 -d > /etc/ssh/ssh_host_ed25519_key.pub <<'__CAPI_EOF__'\n%s\n__CAPI_EOF__\n", base64.StdEncoding.EncodeToString(keys.HostPublicKey))
	b.WriteString(`chmod 600 /etc/ssh/ssh_host_ed25519_key
chmod 644 /etc/ssh/ssh_host_ed25519_key.pub
systemctl restart ssh || systemctl restart sshd

# envoy
`)
	fmt.Fprintf(&b, "for i in 1 2 3 4 5; do curl -fsSL -o /usr/local/bin/envoy https://github.com/envoyproxy/envoy/releases/download/v%s/envoy-%s-linux-x86_64 && break || sleep 10; done\n", EnvoyVersion, EnvoyVersion)
	b.WriteString("chmod 0755 /usr/local/bin/envoy\n")
	fmt.Fprintf(&b, "mkdir -p %s\n", EnvoyDir)
	fmt.Fprintf(&b, "base64 -d > %s/envoy.yaml <<'__CAPI_EOF__'\n%s\n__CAPI_EOF__\n", EnvoyDir, base64.StdEncoding.EncodeToString([]byte(EnvoyBootstrap())))
	fmt.Fprintf(&b, "base64 -d > %s <<'__CAPI_EOF__'\n%s\n__CAPI_EOF__\n", EnvoyLDSPath, base64.StdEncoding.EncodeToString([]byte(lds)))
	fmt.Fprintf(&b, "base64 -d > %s <<'__CAPI_EOF__'\n%s\n__CAPI_EOF__\n", EnvoyCDSPath, base64.StdEncoding.EncodeToString([]byte(cds)))
	b.WriteString(`cat > /etc/systemd/system/envoy.service <<'__CAPI_EOF__'
[Unit]
Description=Envoy service load balancer (cluster-api-provider-verda)
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/envoy -c /etc/envoy/envoy.yaml --log-path /var/log/envoy.log
Restart=always
RestartSec=2
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
__CAPI_EOF__
/usr/local/bin/envoy --mode validate -c /etc/envoy/envoy.yaml
systemctl daemon-reload
systemctl enable envoy
systemctl restart envoy
echo "[capi-lb] finished $(date -Is)"
`)
	return b.String()
}

// EnvoyFiles returns the files to push for a set of frontends. Clusters are
// written before listeners so a new listener never references a missing
// cluster.
func EnvoyFiles(frontends []Frontend) []File {
	lds, cds := EnvoyResources(frontends)
	return []File{{Path: EnvoyCDSPath, Content: cds}, {Path: EnvoyLDSPath, Content: lds}}
}
