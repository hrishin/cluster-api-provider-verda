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

package bootstrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const sampleCloudConfig = `## template: jinja
#cloud-config

write_files:
-   path: /etc/kubernetes/pki/ca.crt
    owner: root:root
    permissions: '0640'
    content: |
      -----BEGIN CERTIFICATE-----
      it's not really a cert
      -----END CERTIFICATE-----
-   path: /run/kubeadm/kubeadm.yaml
    owner: root:root
    permissions: '0640'
    content: |
      ---
      apiVersion: kubeadm.k8s.io/v1beta4
      kind: ClusterConfiguration
-   path: /etc/motd
    append: true
    encoding: base64
    content: aGVsbG8K
runcmd:
  - 'kubeadm init --config /run/kubeadm/kubeadm.yaml  && echo success > /run/cluster-api/bootstrap-success.complete'
  - 'echo "provider-id: verda://{{ ds.meta_data.hostname }}"'
  - [ "echo", "list form", "with 'quotes'" ]
users:
  - name: capi
    sudo: ALL=(ALL) NOPASSWD:ALL
`

func TestToStartupScript(t *testing.T) {
	res, err := ToStartupScript([]byte(sampleCloudConfig), "cp-0")
	if err != nil {
		t.Fatalf("ToStartupScript: %v", err)
	}
	if got, want := res.Unsupported, []string{"users"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("Unsupported = %v, want %v", got, want)
	}
	for _, want := range []string{
		"#!/bin/bash",
		"mkdir -p '/etc/kubernetes/pki'",
		"base64 -d > '/etc/kubernetes/pki/ca.crt' <<'__CAPI_FILE_0__'",
		"chmod '0640' '/etc/kubernetes/pki/ca.crt'",
		"chown 'root:root' '/etc/kubernetes/pki/ca.crt'",
		"base64 -d >> '/etc/motd'",
		"kubeadm init --config /run/kubeadm/kubeadm.yaml",
		`echo "provider-id: verda://cp-0"`,
		`'echo' 'list form' 'with '\''quotes'\'''`,
	} {
		if !strings.Contains(res.Script, want) {
			t.Errorf("script missing %q\n%s", want, res.Script)
		}
	}
}

func TestToStartupScriptPassesThroughShell(t *testing.T) {
	in := "#!/bin/sh\necho hi\n"
	res, err := ToStartupScript([]byte(in), "host")
	if err != nil {
		t.Fatalf("ToStartupScript: %v", err)
	}
	if res.Script != in {
		t.Errorf("script = %q, want %q", res.Script, in)
	}
}

func TestToStartupScriptRejectsUnknownFormat(t *testing.T) {
	if _, err := ToStartupScript([]byte(`{"ignition": {"version": "3.0.0"}}`), "host"); err == nil {
		t.Fatal("expected error for ignition data")
	}
}

// TestScriptExecutes runs the generated script in a sandbox root to check the
// emitted shell is actually valid.
func TestScriptExecutes(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	root := t.TempDir()
	cfg := `#cloud-config
write_files:
-   path: ` + root + `/a/b/plain.txt
    permissions: '0600'
    content: |
      line one
      it's got 'quotes' and $dollars and __CAPI_FILE_0__
-   path: ` + root + `/encoded.txt
    encoding: b64
    content: aGVsbG8gd29ybGQK
runcmd:
  - echo ran > ` + root + `/ran
  - [ "cp", "` + root + `/ran", "` + root + `/ran2" ]
`
	res, err := ToStartupScript([]byte(cfg), "host")
	if err != nil {
		t.Fatalf("ToStartupScript: %v", err)
	}
	// Redirect the log to the sandbox.
	script := strings.Replace(res.Script, LogPath, filepath.Join(root, "log"), 1)
	cmd := exec.Command("bash", "-c", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script failed: %v\n%s\n---\n%s", err, out, script)
	}

	plain, err := os.ReadFile(filepath.Join(root, "a", "b", "plain.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "line one\nit's got 'quotes' and $dollars and __CAPI_FILE_0__\n"; string(plain) != want {
		t.Errorf("plain.txt = %q, want %q", plain, want)
	}
	if info, err := os.Stat(filepath.Join(root, "a", "b", "plain.txt")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("plain.txt mode = %v, err = %v", info.Mode(), err)
	}
	encoded, _ := os.ReadFile(filepath.Join(root, "encoded.txt"))
	if string(encoded) != "hello world\n" {
		t.Errorf("encoded.txt = %q", encoded)
	}
	for _, f := range []string{"ran", "ran2"} {
		if _, err := os.Stat(filepath.Join(root, f)); err != nil {
			t.Errorf("runcmd did not produce %s: %v", f, err)
		}
	}
}
