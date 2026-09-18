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

// In-memory fake of the Verda API mirroring its instance, volume and deletion semantics, for tests.

package cloud

import (
	"context"
	"fmt"
	"maps"
	"sync"
)

type Fake struct {
	mu        sync.Mutex
	nextID    int
	Instances map[string]*Instance
	Scripts   map[string]string

	ScriptNames map[string]string
	Volumes     map[string]*Volume

	InstanceTypes map[string]*InstanceTypeInfo

	Created []InstanceSpec
}

func NewFake() *Fake {
	return &Fake{
		Instances: map[string]*Instance{}, Scripts: map[string]string{}, ScriptNames: map[string]string{}, Volumes: map[string]*Volume{},
		InstanceTypes: map[string]*InstanceTypeInfo{
			"CPU.4V.16G":    {InstanceType: "CPU.4V.16G", CPUs: 4, MemoryGB: 16},
			"1H100.80S.30V": {InstanceType: "1H100.80S.30V", CPUs: 30, MemoryGB: 120, GPUs: 1, GPUModel: "H100"},
		},
	}
}

var _ Client = &Fake{}

func (f *Fake) GetInstanceType(_ context.Context, instanceType string) (*InstanceTypeInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.InstanceTypes[instanceType]
	if !ok {
		return nil, ErrNotFound
	}
	out := *t
	return &out, nil
}

func (f *Fake) GetInstance(_ context.Context, id string) (*Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.Instances[id]
	if !ok {
		return nil, ErrNotFound
	}
	return copyInstance(inst), nil
}

func (f *Fake) FindInstanceByHostname(_ context.Context, hostname string) (*Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var gone *Instance
	for _, inst := range f.Instances {
		if inst.Hostname != hostname {
			continue
		}
		if inst.Status == StatusDiscontinued {
			gone = inst
			continue
		}
		return copyInstance(inst), nil
	}
	if gone != nil {
		return copyInstance(gone), nil
	}
	return nil, ErrNotFound
}

func (f *Fake) FindInstanceByTag(_ context.Context, key, value string) (*Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var gone *Instance
	for _, inst := range f.Instances {
		if inst.Tags[key] != value {
			continue
		}
		if inst.Status == StatusDiscontinued {
			gone = inst
			continue
		}
		return copyInstance(inst), nil
	}
	if gone != nil {
		return copyInstance(gone), nil
	}
	return nil, ErrNotFound
}

func (f *Fake) CreateInstance(_ context.Context, spec InstanceSpec) (*Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.Instances {
		if existing.Hostname == spec.Hostname && existing.Status != StatusDiscontinued && existing.Tags[TagManagedBy] != ManagedByValue {
			return nil, fmt.Errorf("instance %s: %w", existing.ID, ErrHostnameInUse)
		}
	}
	for id, name := range f.ScriptNames {
		if name == spec.Hostname {
			delete(f.Scripts, id)
			delete(f.ScriptNames, id)
		}
	}
	f.Created = append(f.Created, spec)
	f.nextID++
	id := fmt.Sprintf("inst-%d", f.nextID)
	scriptID := ""
	if spec.StartupScript != "" {
		scriptID = fmt.Sprintf("script-%d", f.nextID)
		f.Scripts[scriptID] = spec.StartupScript
		f.ScriptNames[scriptID] = spec.Hostname
	}
	inst := &Instance{
		ID:              id,
		Hostname:        spec.Hostname,
		Status:          "provisioning",
		Location:        spec.Location,
		InstanceType:    spec.InstanceType,
		StartupScriptID: scriptID,
		Tags:            maps.Clone(spec.Tags),
	}

	if vol, ok := f.Volumes[spec.Image]; ok {
		vol.Status = "attached"
		vol.InstanceID = id
		inst.OSVolumeID = spec.Image
	}
	f.Instances[id] = inst
	return copyInstance(inst), nil
}

func (f *Fake) DeleteInstance(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst, ok := f.Instances[id]
	if ok && inst.Tags[TagManagedBy] != ManagedByValue {
		return ErrNotManaged
	}
	if ok {

		delete(f.Volumes, inst.OSVolumeID)
		inst.Status = StatusDiscontinued
	}
	return nil
}

func (f *Fake) PurgeInstanceVolumes(_ context.Context, id string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if inst, ok := f.Instances[id]; ok {
		delete(f.Volumes, inst.OSVolumeID)
	}
	return 0, nil
}

func (f *Fake) DeleteStartupScript(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.Scripts, id)
	delete(f.ScriptNames, id)
	return nil
}

func (f *Fake) DeleteStartupScriptByName(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, n := range f.ScriptNames {
		if n == name {
			delete(f.Scripts, id)
			delete(f.ScriptNames, id)
		}
	}
	return nil
}

func (f *Fake) GetVolume(_ context.Context, id string) (*Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	vol, ok := f.Volumes[id]
	if !ok {
		return nil, ErrNotFound
	}
	out := *vol
	return &out, nil
}

func (f *Fake) FindVolumeByName(_ context.Context, name string) (*Volume, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, vol := range f.Volumes {
		if vol.Name == name {
			out := *vol
			return &out, nil
		}
	}
	return nil, ErrNotFound
}

func (f *Fake) CloneVolume(_ context.Context, sourceID, name, location string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	src, ok := f.Volumes[sourceID]
	if !ok {
		return "", ErrNotFound
	}
	f.nextID++
	id := fmt.Sprintf("vol-%d", f.nextID)
	f.Volumes[id] = &Volume{ID: id, Name: name, Status: "cloning", Location: location, IsOSVolume: src.IsOSVolume, Managed: true}
	return id, nil
}

func (f *Fake) EnsureVolumeTag(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if vol, ok := f.Volumes[id]; ok {
		vol.Managed = true
	}
	return nil
}

func (f *Fake) DeleteVolume(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if vol, ok := f.Volumes[id]; ok && !vol.Managed {
		return ErrNotManaged
	}
	delete(f.Volumes, id)
	return nil
}

func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Created = nil
}

func (f *Fake) SetVolumeStatus(id, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if vol, ok := f.Volumes[id]; ok {
		vol.Status = status
	}
}

func (f *Fake) SetStatus(id, status, ip string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if inst, ok := f.Instances[id]; ok {
		inst.Status = status
		inst.IP = ip
	}
}

func copyInstance(in *Instance) *Instance {
	out := *in
	out.Tags = maps.Clone(in.Tags)
	return &out
}
