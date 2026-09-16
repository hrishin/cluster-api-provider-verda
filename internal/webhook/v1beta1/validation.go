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

// Package v1beta1 holds the admission webhooks for the infrastructure v1beta1 API.
package v1beta1

import (
	"regexp"

	"k8s.io/apimachinery/pkg/util/validation/field"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
)

var locationCodeRe = regexp.MustCompile(`^[A-Z]{3}-[0-9]{2}$`)

// Fields of a VerdaMachine that cannot change once the instance exists.
var immutableMachineFields = []string{"instanceType", "image", "osVolumeID", "osVolumeSizeGB", "contract", "spot"}

// validateMachineSpec checks a VerdaMachineSpec on its own.
func validateMachineSpec(spec *infrav1.VerdaMachineSpec, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	if spec.InstanceType == "" {
		errs = append(errs, field.Required(path.Child("instanceType"), "instanceType is required"))
	}
	switch {
	case spec.Image == "" && spec.OSVolumeID == "":
		errs = append(errs, field.Required(path.Child("image"), "one of image or osVolumeID is required"))
	case spec.Image != "" && spec.OSVolumeID != "":
		errs = append(errs, field.Forbidden(path.Child("osVolumeID"), "osVolumeID cannot be set together with image"))
	}
	if spec.OSVolumeID != "" && spec.OSVolumeSizeGB != 0 {
		errs = append(errs, field.Forbidden(path.Child("osVolumeSizeGB"), "osVolumeSizeGB cannot be set with osVolumeID; a clone keeps the size of its source"))
	}
	if spec.Spot != nil && *spec.Spot && spec.Contract != "" && spec.Contract != "SPOT" {
		errs = append(errs, field.Invalid(path.Child("contract"), spec.Contract, "contract must be SPOT or empty when spot is true"))
	}
	return errs
}

// validateMachineSpecUpdate rejects changes to fields that would require a
// new instance. providerID may be set once (by the controller) but not changed.
func validateMachineSpecUpdate(oldSpec, newSpec *infrav1.VerdaMachineSpec, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	if oldSpec.InstanceType != newSpec.InstanceType {
		errs = append(errs, field.Forbidden(path.Child("instanceType"), "instanceType is immutable"))
	}
	if oldSpec.Image != newSpec.Image {
		errs = append(errs, field.Forbidden(path.Child("image"), "image is immutable"))
	}
	if oldSpec.OSVolumeID != newSpec.OSVolumeID {
		errs = append(errs, field.Forbidden(path.Child("osVolumeID"), "osVolumeID is immutable"))
	}
	if oldSpec.OSVolumeSizeGB != newSpec.OSVolumeSizeGB {
		errs = append(errs, field.Forbidden(path.Child("osVolumeSizeGB"), "osVolumeSizeGB is immutable"))
	}
	if oldSpec.Contract != newSpec.Contract {
		errs = append(errs, field.Forbidden(path.Child("contract"), "contract is immutable"))
	}
	if boolValue(oldSpec.Spot) != boolValue(newSpec.Spot) {
		errs = append(errs, field.Forbidden(path.Child("spot"), "spot is immutable"))
	}
	if oldSpec.ProviderID != "" && oldSpec.ProviderID != newSpec.ProviderID {
		errs = append(errs, field.Forbidden(path.Child("providerID"), "providerID cannot be changed once set"))
	}
	return errs
}

// validateClusterSpec checks a VerdaClusterSpec on its own.
func validateClusterSpec(spec *infrav1.VerdaClusterSpec, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	if spec.Location == "" {
		errs = append(errs, field.Required(path.Child("location"), "location is required"))
	} else if !locationCodeRe.MatchString(spec.Location) {
		errs = append(errs, field.Invalid(path.Child("location"), spec.Location, "must be a Verda location code such as FIN-01"))
	}
	if spec.ControlPlaneEndpoint.Host != "" && spec.ControlPlaneEndpoint.Port == 0 {
		// Defaulted to 6443 by the mutating webhook; reaching here means defaulting was bypassed.
		errs = append(errs, field.Required(path.Child("controlPlaneEndpoint", "port"), "port is required when host is set"))
	}
	lb := spec.ControlPlaneLoadBalancer
	if lb.Enabled != nil && !*lb.Enabled && (lb.InstanceType != "" || lb.Image != "" || len(lb.SSHKeyIDs) > 0) {
		errs = append(errs, field.Invalid(path.Child("controlPlaneLoadBalancer", "enabled"), false, "load balancer settings are set but enabled is false"))
	}
	return errs
}

// validateClusterSpecUpdate rejects changes that would move or re-front the cluster.
func validateClusterSpecUpdate(oldSpec, newSpec *infrav1.VerdaClusterSpec, path *field.Path) field.ErrorList {
	var errs field.ErrorList
	if oldSpec.Location != newSpec.Location {
		errs = append(errs, field.Forbidden(path.Child("location"), "location is immutable"))
	}
	if boolValue(oldSpec.ControlPlaneLoadBalancer.Enabled) != boolValue(newSpec.ControlPlaneLoadBalancer.Enabled) {
		errs = append(errs, field.Forbidden(path.Child("controlPlaneLoadBalancer", "enabled"), "the load balancer cannot be enabled or disabled after creation; the control plane endpoint is baked into the cluster's certificates"))
	}
	// The endpoint is part of the API server certificate and every kubeconfig:
	// once set it cannot change. It may be set once (by the user or, with the
	// load balancer, by the controller).
	if oldSpec.ControlPlaneEndpoint.Host != "" && oldSpec.ControlPlaneEndpoint != newSpec.ControlPlaneEndpoint {
		errs = append(errs, field.Forbidden(path.Child("controlPlaneEndpoint"), "controlPlaneEndpoint is immutable once set"))
	}
	return errs
}

func boolValue(b *bool) bool { return b != nil && *b }
