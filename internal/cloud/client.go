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

// Package cloud wraps the Verda SDK behind a small interface so that the
// controllers depend only on what they use and can be tested against a fake.
package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/verda-cloud/verdacloud-sdk-go/pkg/verda"
)

// ErrNotFound is returned when the requested resource does not exist in Verda.
var ErrNotFound = errors.New("resource not found")

// ErrNotManaged is returned when asked to delete a resource this provider did not create.
var ErrNotManaged = errors.New("resource is not managed by cluster-api-provider-verda")

// ErrHostnameInUse is returned by CreateInstance when a live instance that this
// provider does not own already uses the requested hostname.
var ErrHostnameInUse = errors.New("hostname is in use by an instance not managed by cluster-api-provider-verda")

// Tag keys used to correlate Verda resources with Cluster API objects.
// Verda lowercases keys, so they are kept lowercase here.
const (
	// TagCluster holds the namespaced name of the owning CAPI Cluster.
	TagCluster = "capi-cluster"
	// TagMachine holds the namespaced name of the owning VerdaMachine.
	TagMachine = "capi-machine"
	// TagLoadBalancer holds the namespaced name of the VerdaCluster whose
	// control plane load balancer the instance is.
	TagLoadBalancer = "capi-loadbalancer"
	// TagManagedBy identifies resources created by this provider.
	TagManagedBy = "capi-managed-by"
	// ManagedByValue is the value of TagManagedBy.
	ManagedByValue = "cluster-api-provider-verda"
)

// Instance is the subset of a Verda instance the provider cares about.
type Instance struct {
	ID              string
	Hostname        string
	Status          string
	IP              string
	Location        string
	InstanceType    string
	StartupScriptID string
	OSVolumeID      string
	Tags            map[string]string
}

// Volume is the subset of a Verda volume the provider cares about.
type Volume struct {
	ID         string
	Name       string
	Status     string
	Location   string
	IsOSVolume bool
	InstanceID string
	// Managed is true when the volume carries this provider's TagManagedBy tag.
	Managed bool
}

// InstanceSpec describes an instance to create.
type InstanceSpec struct {
	Hostname     string
	Description  string
	InstanceType string
	// Image is an image slug, or the ID of a detached OS volume to boot from.
	Image          string
	Location       string
	SSHKeyIDs      []string
	OSVolumeSizeGB int32
	Contract       string
	Spot           bool
	// SpotDiscontinuePolicy applies to the OS and data volumes of spot instances.
	SpotDiscontinuePolicy string
	// DataVolumes are created with and attached to the instance.
	DataVolumes []DataVolumeSpec
	// StartupScript is the script executed on first boot. It carries the
	// Cluster API bootstrap data.
	StartupScript string
	Tags          map[string]string
}

// DataVolumeSpec describes a data volume to create with an instance.
type DataVolumeSpec struct {
	Name   string
	SizeGB int32
	Type   string
}

// InstanceTypeInfo is the subset of the Verda instance type catalog used for
// node capacity.
type InstanceTypeInfo struct {
	InstanceType string
	CPUs         int
	MemoryGB     int
	GPUs         int
	GPUModel     string
}

// Client is the set of Verda operations used by the controllers.
type Client interface {
	// GetInstanceType returns catalog information for an instance type, or ErrNotFound.
	GetInstanceType(ctx context.Context, instanceType string) (*InstanceTypeInfo, error)
	// GetInstance returns the instance with the given ID, or ErrNotFound.
	GetInstance(ctx context.Context, id string) (*Instance, error)
	// FindInstanceByHostname returns the live instance with the given hostname,
	// or a discontinued one if that is all there is, or ErrNotFound.
	FindInstanceByHostname(ctx context.Context, hostname string) (*Instance, error)
	// FindInstanceByTag returns the first instance carrying key=value, or ErrNotFound.
	// It is used to recover from a create whose result was never persisted.
	FindInstanceByTag(ctx context.Context, key, value string) (*Instance, error)
	// CreateInstance creates the startup script and the instance described by
	// spec. A startup script left behind by an earlier attempt (same name) is
	// replaced. It fails with ErrHostnameInUse if a live instance not owned by
	// this provider already has spec.Hostname.
	CreateInstance(ctx context.Context, spec InstanceSpec) (*Instance, error)
	// DeleteStartupScriptByName deletes any startup scripts with the given
	// name. Used to clean up scripts whose ID was never recorded.
	DeleteStartupScriptByName(ctx context.Context, name string) error
	// DeleteInstance deletes the instance and its OS volume. Deleting a missing
	// instance is not an error. Instances without the TagManagedBy tag are never
	// deleted (ErrNotManaged).
	DeleteInstance(ctx context.Context, id string) error
	// DeleteStartupScript deletes a startup script. Deleting a missing script is not an error.
	DeleteStartupScript(ctx context.Context, id string) error
	// GetVolume returns the volume with the given ID, or ErrNotFound.
	GetVolume(ctx context.Context, id string) (*Volume, error)
	// FindVolumeByName returns the first volume with the given name, or ErrNotFound.
	FindVolumeByName(ctx context.Context, name string) (*Volume, error)
	// CloneVolume clones sourceID into a new volume called name in location and
	// returns the new volume's ID. The clone is not usable until its status is "detached".
	CloneVolume(ctx context.Context, sourceID, name, location string) (string, error)
	// DeleteVolume permanently deletes a volume. Deleting a missing volume is not
	// an error. Volumes not tagged TagManagedBy are never deleted (ErrNotManaged).
	DeleteVolume(ctx context.Context, id string) error
	// EnsureVolumeTag tags a volume as managed by this provider. Idempotent.
	EnsureVolumeTag(ctx context.Context, id string) error
}

// sdkClient implements Client on top of the Verda SDK.
type sdkClient struct {
	api *verda.Client
}

// NewClient builds a Client from the given credentials.
func NewClient(creds Credentials) (Client, error) {
	opts := []verda.ClientOption{
		verda.WithClientID(creds.ClientID),
		verda.WithClientSecret(creds.ClientSecret),
		verda.WithUserAgent("cluster-api-provider-verda"),
	}
	if creds.BaseURL != "" {
		opts = append(opts, verda.WithBaseURL(creds.BaseURL))
	}
	api, err := verda.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("creating Verda client: %w", err)
	}
	return &sdkClient{api: api}, nil
}

func (c *sdkClient) GetInstanceType(ctx context.Context, instanceType string) (*InstanceTypeInfo, error) {
	types, err := c.api.InstanceTypes.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("listing instance types: %w", err)
	}
	for i := range types {
		t := &types[i]
		if t.InstanceType != instanceType {
			continue
		}
		return &InstanceTypeInfo{
			InstanceType: t.InstanceType,
			CPUs:         t.CPU.NumberOfCores,
			MemoryGB:     t.Memory.SizeInGigabytes,
			GPUs:         t.GPU.NumberOfGPUs,
			GPUModel:     t.Model,
		}, nil
	}
	return nil, ErrNotFound
}

func (c *sdkClient) GetInstance(ctx context.Context, id string) (*Instance, error) {
	inst, err := c.api.Instances.GetByID(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("getting instance %s: %w", id, err)
	}
	return toInstance(inst), nil
}

func (c *sdkClient) FindInstanceByHostname(ctx context.Context, hostname string) (*Instance, error) {
	instances, err := c.api.Instances.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}
	var gone *Instance
	for i := range instances {
		inst := toInstance(&instances[i])
		if inst.Hostname != hostname {
			continue
		}
		if inst.Status == verda.StatusDiscontinued {
			gone = inst
			continue
		}
		return inst, nil
	}
	if gone != nil {
		return gone, nil
	}
	return nil, ErrNotFound
}

func (c *sdkClient) FindInstanceByTag(ctx context.Context, key, value string) (*Instance, error) {
	instances, err := c.api.Instances.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}
	// Prefer a live instance; discontinued ones stay listed for a while.
	var gone *Instance
	for i := range instances {
		inst := toInstance(&instances[i])
		if inst.Tags[key] != value {
			continue
		}
		if inst.Status == verda.StatusDiscontinued {
			gone = inst
			continue
		}
		return inst, nil
	}
	if gone != nil {
		return gone, nil
	}
	return nil, ErrNotFound
}

func (c *sdkClient) CreateInstance(ctx context.Context, spec InstanceSpec) (*Instance, error) {
	// Hostnames are the node names and the provider IDs; never take over one
	// that belongs to something else in the account.
	instances, err := c.api.Instances.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}
	for i := range instances {
		existing := toInstance(&instances[i])
		if existing.Hostname == spec.Hostname && existing.Status != verda.StatusDiscontinued && existing.Tags[TagManagedBy] != ManagedByValue {
			return nil, fmt.Errorf("instance %s: %w", existing.ID, ErrHostnameInUse)
		}
	}
	// A script from an interrupted earlier attempt would otherwise leak.
	if err := c.DeleteStartupScriptByName(ctx, spec.Hostname); err != nil {
		return nil, err
	}

	req := verda.CreateInstanceRequest{
		InstanceType: spec.InstanceType,
		Image:        spec.Image,
		Hostname:     spec.Hostname,
		Description:  spec.Description,
		SSHKeyIDs:    spec.SSHKeyIDs,
		LocationCode: spec.Location,
		Contract:     spec.Contract,
		IsSpot:       spec.Spot,
	}
	if spec.OSVolumeSizeGB > 0 {
		req.OSVolume = &verda.OSVolumeCreateRequest{
			Name:              spec.Hostname + "-os",
			Size:              int(spec.OSVolumeSizeGB),
			OnSpotDiscontinue: spotPolicy(spec),
		}
	}
	for _, dv := range spec.DataVolumes {
		volType := dv.Type
		if volType == "" {
			volType = verda.VolumeTypeNVMe
		}
		req.Volumes = append(req.Volumes, verda.VolumeCreateRequest{
			Name:              spec.Hostname + "-" + dv.Name,
			Size:              int(dv.SizeGB),
			Type:              volType,
			OnSpotDiscontinue: spotPolicy(spec),
		})
	}
	for k, v := range spec.Tags {
		req.Tags = append(req.Tags, verda.TagRequest{Key: k, Value: v})
	}

	if spec.StartupScript != "" {
		script, err := c.api.StartupScripts.AddStartupScript(ctx, &verda.CreateStartupScriptRequest{
			Name:   spec.Hostname,
			Script: spec.StartupScript,
		})
		if err != nil {
			return nil, fmt.Errorf("creating startup script for %s: %w", spec.Hostname, err)
		}
		req.StartupScriptID = &script.ID
	}

	inst, err := c.api.Instances.Create(ctx, req)
	if err != nil {
		// Don't leak the script if the instance was never created.
		if req.StartupScriptID != nil {
			_ = c.api.StartupScripts.DeleteStartupScript(ctx, *req.StartupScriptID)
		}
		return nil, fmt.Errorf("creating instance %s: %w", spec.Hostname, err)
	}
	out := toInstance(inst)
	// The create response is sometimes just an ID; carry over what we know.
	if out.StartupScriptID == "" && req.StartupScriptID != nil {
		out.StartupScriptID = *req.StartupScriptID
	}
	return out, nil
}

// spotPolicy returns the on_spot_discontinue value to send, if any.
func spotPolicy(spec InstanceSpec) string {
	if !spec.Spot {
		return ""
	}
	if spec.SpotDiscontinuePolicy == "" {
		return verda.SpotDiscontinueDeletePermanent
	}
	return spec.SpotDiscontinuePolicy
}

func (c *sdkClient) DeleteInstance(ctx context.Context, id string) error {
	inst, err := c.api.Instances.GetByID(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("getting instance %s before delete: %w", id, err)
	}
	// Safety net: never delete instances this provider did not create.
	if toInstance(inst).Tags[TagManagedBy] != ManagedByValue {
		return fmt.Errorf("instance %s (%s): %w", id, inst.Hostname, ErrNotManaged)
	}
	// Delete every volume that came with the instance: the OS volume (or its
	// clone) and the data volumes. All were created by this provider.
	var volumes []string
	if inst.OSVolumeID != nil {
		volumes = append(volumes, *inst.OSVolumeID)
	}
	volumes = append(volumes, inst.VolumeIDs...)
	if err := c.api.Instances.Delete(ctx, []string{id}, volumes, true); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting instance %s: %w", id, err)
	}
	return nil
}

func (c *sdkClient) DeleteStartupScriptByName(ctx context.Context, name string) error {
	scripts, err := c.api.StartupScripts.GetAllStartupScripts(ctx)
	if err != nil {
		return fmt.Errorf("listing startup scripts: %w", err)
	}
	for _, script := range scripts {
		if script.Name != name {
			continue
		}
		if err := c.DeleteStartupScript(ctx, script.ID); err != nil {
			return err
		}
	}
	return nil
}

func (c *sdkClient) DeleteStartupScript(ctx context.Context, id string) error {
	if err := c.api.StartupScripts.DeleteStartupScript(ctx, id); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting startup script %s: %w", id, err)
	}
	return nil
}

func (c *sdkClient) GetVolume(ctx context.Context, id string) (*Volume, error) {
	vol, err := c.api.Volumes.GetVolume(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("getting volume %s: %w", id, err)
	}
	return toVolume(vol), nil
}

func (c *sdkClient) FindVolumeByName(ctx context.Context, name string) (*Volume, error) {
	volumes, err := c.api.Volumes.ListVolumes(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing volumes: %w", err)
	}
	for i := range volumes {
		if volumes[i].Name == name {
			return toVolume(&volumes[i]), nil
		}
	}
	return nil, ErrNotFound
}

func (c *sdkClient) CloneVolume(ctx context.Context, sourceID, name, location string) (string, error) {
	id, err := c.api.Volumes.CloneVolume(ctx, sourceID, verda.VolumeCloneRequest{Name: name, LocationCode: location})
	if err != nil {
		return "", fmt.Errorf("cloning volume %s as %s: %w", sourceID, name, err)
	}
	// Mark the clone as ours so DeleteVolume will accept it. Tagging can fail
	// while the clone is still being created; the controller retries via
	// EnsureVolumeTag.
	_ = c.tagVolume(ctx, id)
	return id, nil
}

// EnsureVolumeTag tags a volume as managed by this provider (idempotent).
func (c *sdkClient) EnsureVolumeTag(ctx context.Context, id string) error {
	return c.tagVolume(ctx, id)
}

func (c *sdkClient) tagVolume(ctx context.Context, id string) error {
	_, err := c.api.Volumes.AddTag(ctx, id, verda.TagRequest{Key: TagManagedBy, Value: ManagedByValue})
	var apiErr *verda.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
		return nil // already tagged
	}
	return err
}

func (c *sdkClient) DeleteVolume(ctx context.Context, id string) error {
	vol, err := c.api.Volumes.GetVolume(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("getting volume %s before delete: %w", id, err)
	}
	if !hasTag(vol.Tags, TagManagedBy, ManagedByValue) {
		return fmt.Errorf("volume %s (%s): %w", id, vol.Name, ErrNotManaged)
	}
	if err := c.api.Volumes.DeleteVolume(ctx, id, true); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting volume %s: %w", id, err)
	}
	return nil
}

func hasTag(tags []verda.Tag, key, value string) bool {
	for _, t := range tags {
		if t.Key == key && t.Value == value {
			return true
		}
	}
	return false
}

func toVolume(in *verda.Volume) *Volume {
	out := &Volume{
		ID:         in.ID,
		Name:       in.Name,
		Status:     in.Status,
		Location:   in.Location,
		IsOSVolume: in.IsOSVolume,
		Managed:    hasTag(in.Tags, TagManagedBy, ManagedByValue),
	}
	if in.InstanceID != nil {
		out.InstanceID = *in.InstanceID
	}
	return out
}

func toInstance(in *verda.Instance) *Instance {
	out := &Instance{
		ID:           in.ID,
		Hostname:     in.Hostname,
		Status:       in.Status,
		Location:     in.Location,
		InstanceType: in.InstanceType,
		Tags:         make(map[string]string, len(in.Tags)),
	}
	if in.IP != nil {
		out.IP = *in.IP
	}
	if in.StartupScriptID != nil {
		out.StartupScriptID = *in.StartupScriptID
	}
	if in.OSVolumeID != nil {
		out.OSVolumeID = *in.OSVolumeID
	}
	for _, t := range in.Tags {
		out.Tags[t.Key] = t.Value
	}
	return out
}

func isNotFound(err error) bool {
	var apiErr *verda.APIError
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}
