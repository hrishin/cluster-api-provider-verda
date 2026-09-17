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

package cloud

import (
	"slices"
	"testing"

	"github.com/verda-cloud/verdacloud-sdk-go/pkg/verda"
	"k8s.io/utils/ptr"
)

// Verda lists the OS volume in volume_ids too; a duplicate in the delete
// request makes Verda accept but never execute the delete.
func TestInstanceVolumeIDsDeduplicates(t *testing.T) {
	inst := &verda.Instance{OSVolumeID: ptr.To("os"), VolumeIDs: []string{"os", "data", "data"}}
	if got := instanceVolumeIDs(inst); !slices.Equal(got, []string{"os", "data"}) {
		t.Errorf("got %v", got)
	}
	if got := instanceVolumeIDs(&verda.Instance{}); len(got) != 0 {
		t.Errorf("got %v for an instance without volumes", got)
	}
}
