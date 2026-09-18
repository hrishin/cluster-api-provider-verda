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

// envoy configuration and startup script for the service load balancer instance.

package loadbalancer

import (
	"slices"
	"strings"
)

const (
	EnvoyVersion = "1.34.1"

	EnvoyDir = "/etc/envoy"

	EnvoyLDSPath = EnvoyDir + "/lds.yaml"
	EnvoyCDSPath = EnvoyDir + "/cds.yaml"
)

type Frontend struct {
	Name string

	Protocol string

	Port int32

	Backends []Backend

	HealthCheckPort int32

	ProxyProtocol bool
}

type Backend struct {
	Address string
	Port    int32
}

func EnvoyBootstrap() string {
	return render("envoy-bootstrap.yaml.tmpl", map[string]string{"Dir": EnvoyDir, "LDSPath": EnvoyLDSPath, "CDSPath": EnvoyCDSPath})
}

type frontendView struct {
	Frontend
	UDP bool
}

func EnvoyResources(frontends []Frontend) (lds, cds string) {
	sorted := slices.Clone(frontends)
	slices.SortFunc(sorted, func(a, b Frontend) int { return strings.Compare(a.Name, b.Name) })
	views := make([]frontendView, 0, len(sorted))
	for _, f := range sorted {
		f.Backends = slices.Clone(f.Backends)
		slices.SortFunc(f.Backends, func(a, b Backend) int { return strings.Compare(a.Address, b.Address) })
		views = append(views, frontendView{Frontend: f, UDP: strings.EqualFold(f.Protocol, "UDP")})
	}
	return render("envoy-lds.yaml.tmpl", views), render("envoy-cds.yaml.tmpl", views)
}

func EnvoyStartupScript(keys *Keys) string {
	lds, cds := EnvoyResources(nil)
	return render("envoy-startup.sh.tmpl", map[string]string{
		"ClientPublicKey": b64(keys.ClientPublicKey),
		"HostPrivateKey":  b64(keys.HostPrivateKey),
		"HostPublicKey":   b64(keys.HostPublicKey),
		"Version":         EnvoyVersion,
		"Dir":             EnvoyDir,
		"LDSPath":         EnvoyLDSPath,
		"CDSPath":         EnvoyCDSPath,
		"Bootstrap":       b64([]byte(EnvoyBootstrap())),
		"LDS":             b64([]byte(lds)),
		"CDS":             b64([]byte(cds)),
	})
}

func EnvoyFiles(frontends []Frontend) []File {
	lds, cds := EnvoyResources(frontends)
	return []File{{Path: EnvoyCDSPath, Content: cds}, {Path: EnvoyLDSPath, Content: lds}}
}
