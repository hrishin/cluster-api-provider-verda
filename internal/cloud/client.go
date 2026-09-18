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

// Verda API client used by the controllers: instances, startup scripts, volumes, tags and two-phase deletion.

package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/verda-cloud/verdacloud-sdk-go/pkg/verda"
)

var ErrNotFound = errors.New("resource not found")

var ErrNotManaged = errors.New("resource is not managed by cluster-api-provider-verda")

var ErrHostnameInUse = errors.New("hostname is in use by an instance not managed by cluster-api-provider-verda")

const (
	TagCluster = "capi-cluster"

	TagMachine = "capi-machine"

	TagLoadBalancer = "capi-loadbalancer"

	TagServiceLoadBalancer = "capi-service-loadbalancer"

	TagManagedBy = "capi-managed-by"

	TagRole = "capi-role"

	ManagedByValue = "cluster-api-provider-verda"
)

const (
	StatusRunning      = "running"
	StatusOffline      = "offline"
	StatusDeleting     = "deleting"
	StatusDiscontinued = "discontinued"
	StatusDeleted      = "deleted"
	StatusError        = "error"
	StatusNoCapacity   = "no_capacity"

	StatusNotFound = "notfound"
)

const (
	VolumeStatusDetached = "detached"
	VolumeStatusAttached = "attached"
	VolumeStatusDeleting = "deleting"
	VolumeStatusDeleted  = "deleted"
)

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

type Volume struct {
	ID         string
	Name       string
	Status     string
	Location   string
	IsOSVolume bool
	InstanceID string

	Managed bool
}

type InstanceSpec struct {
	Hostname     string
	Description  string
	InstanceType string

	Image          string
	Location       string
	SSHKeyIDs      []string
	OSVolumeSizeGB int32
	Contract       string
	Spot           bool

	SpotDiscontinuePolicy string

	DataVolumes []DataVolumeSpec

	StartupScript string
	Tags          map[string]string
}

type DataVolumeSpec struct {
	Name   string
	SizeGB int32
	Type   string
}

type InstanceTypeInfo struct {
	InstanceType string
	CPUs         int
	MemoryGB     int
	GPUs         int
	GPUModel     string
}

type Client interface {
	GetInstanceType(ctx context.Context, instanceType string) (*InstanceTypeInfo, error)

	GetInstance(ctx context.Context, id string) (*Instance, error)

	FindInstanceByHostname(ctx context.Context, hostname string) (*Instance, error)

	FindInstanceByTag(ctx context.Context, key, value string) (*Instance, error)

	CreateInstance(ctx context.Context, spec InstanceSpec) (*Instance, error)

	DeleteStartupScriptByName(ctx context.Context, name string) error

	DeleteInstance(ctx context.Context, id string) error

	PurgeInstanceVolumes(ctx context.Context, id string) (pending int, err error)

	DeleteStartupScript(ctx context.Context, id string) error

	GetVolume(ctx context.Context, id string) (*Volume, error)

	FindVolumeByName(ctx context.Context, name string) (*Volume, error)

	CloneVolume(ctx context.Context, sourceID, name, location string) (string, error)

	DeleteVolume(ctx context.Context, id string) error

	EnsureVolumeTag(ctx context.Context, id string) error
}

type sdkClient struct {
	api *verda.Client
}

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
		if inst.Status == StatusDiscontinued {
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

	var gone *Instance
	for i := range instances {
		inst := toInstance(&instances[i])
		if inst.Tags[key] != value {
			continue
		}
		if inst.Status == StatusDiscontinued {
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

	instances, err := c.api.Instances.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("listing instances: %w", err)
	}
	for i := range instances {
		existing := toInstance(&instances[i])
		if existing.Hostname == spec.Hostname && existing.Status != StatusDiscontinued && existing.Tags[TagManagedBy] != ManagedByValue {
			return nil, fmt.Errorf("instance %s: %w", existing.ID, ErrHostnameInUse)
		}
	}

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

		if req.StartupScriptID != nil {
			_ = c.api.StartupScripts.DeleteStartupScript(ctx, *req.StartupScriptID)
		}
		return nil, fmt.Errorf("creating instance %s: %w", spec.Hostname, err)
	}
	out := toInstance(inst)

	if out.StartupScriptID == "" && req.StartupScriptID != nil {
		out.StartupScriptID = *req.StartupScriptID
	}
	return out, nil
}

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

	if toInstance(inst).Tags[TagManagedBy] != ManagedByValue {
		return fmt.Errorf("instance %s (%s): %w", id, inst.Hostname, ErrNotManaged)
	}
	switch inst.Status {
	case StatusDiscontinued, StatusDeleted:
		_, err := c.PurgeInstanceVolumes(ctx, id)
		return err
	case StatusDeleting:
		return nil
	case StatusOffline:

	default:

		if err := c.api.Instances.Shutdown(ctx, id); err != nil {
			if isNotFound(err) {
				return nil
			}
			return fmt.Errorf("shutting down instance %s: %w", id, err)
		}
		return nil
	}

	if err := c.api.Instances.Delete(ctx, []string{id}, instanceVolumeIDs(inst), false); err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("deleting instance %s: %w", id, err)
	}
	return nil
}

func instanceVolumeIDs(inst *verda.Instance) []string {
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if inst.OSVolumeID != nil {
		add(*inst.OSVolumeID)
	}
	for _, id := range inst.VolumeIDs {
		add(id)
	}
	return ids
}

func (c *sdkClient) PurgeInstanceVolumes(ctx context.Context, id string) (int, error) {
	inst, err := c.api.Instances.GetByID(ctx, id)
	if err != nil {
		if isNotFound(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("getting instance %s: %w", id, err)
	}
	if toInstance(inst).Tags[TagManagedBy] != ManagedByValue {
		return 0, fmt.Errorf("instance %s (%s): %w", id, inst.Hostname, ErrNotManaged)
	}
	ids := instanceVolumeIDs(inst)

	trash, err := c.api.Volumes.GetVolumesInTrash(ctx)
	if err != nil {
		return 0, fmt.Errorf("listing trashed volumes: %w", err)
	}
	inTrash := map[string]bool{}
	for _, v := range trash {
		inTrash[v.ID] = true
	}

	pending := 0
	for _, volID := range ids {
		if inTrash[volID] {
			if err := c.api.Volumes.DeleteVolume(ctx, volID, true); err != nil && !isNotFound(err) {
				return 0, fmt.Errorf("purging volume %s: %w", volID, err)
			}
			continue
		}

		vol, err := c.api.Volumes.GetVolume(ctx, volID)
		switch {
		case err == nil && vol.Status != VolumeStatusDeleted:
			pending++
		case err != nil && !isNotFound(err):
			return 0, fmt.Errorf("getting volume %s: %w", volID, err)
		}
	}
	return pending, nil
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

	_ = c.tagVolume(ctx, id)
	return id, nil
}

func (c *sdkClient) EnsureVolumeTag(ctx context.Context, id string) error {
	return c.tagVolume(ctx, id)
}

func (c *sdkClient) tagVolume(ctx context.Context, id string) error {
	_, err := c.api.Volumes.AddTag(ctx, id, verda.TagRequest{Key: TagManagedBy, Value: ManagedByValue})
	var apiErr *verda.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict {
		return nil
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
