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

// Package bootstrap converts Cluster API bootstrap data into a Verda startup script.
//
// Verda instances take a plain shell script at boot rather than cloud-init
// user-data, while the kubeadm bootstrap provider produces a #cloud-config
// document. This package translates the subset of cloud-config that the
// kubeadm bootstrap provider emits (bootcmd, write_files, runcmd) into bash,
// in the same spirit as the Cluster API Docker provider.
package bootstrap

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

// MaxScriptSize is the largest startup script the Verda API accepts (measured:
// 48896 bytes accepted, 49152 rejected).
const MaxScriptSize = 48 * 1024

// compressThreshold is the script size above which the generated script is
// shipped gzip-compressed inside a small decompressing wrapper.
const compressThreshold = 24 * 1024

// ErrScriptTooLarge is returned when even the compressed script exceeds MaxScriptSize.
var ErrScriptTooLarge = fmt.Errorf("startup script exceeds Verda's %d byte limit", MaxScriptSize)

const cloudConfigHeader = "#cloud-config"

// LogPath is where the generated script records its output on the instance.
const LogPath = "/var/log/capi-bootstrap.log"

// cloudConfig is the subset of cloud-config understood by this package.
// Unknown top-level keys are collected and reported, not silently dropped.
type cloudConfig struct {
	BootCmd    []command   `json:"bootcmd,omitempty"`
	WriteFiles []writeFile `json:"write_files,omitempty"`
	RunCmd     []command   `json:"runcmd,omitempty"`
	Users      []user      `json:"users,omitempty"`
	NTP        *ntp        `json:"ntp,omitempty"`
}

var knownKeys = map[string]bool{"bootcmd": true, "write_files": true, "runcmd": true, "users": true, "ntp": true}

// user is the subset of cloud-init's users module that the kubeadm bootstrap
// provider emits (KubeadmConfigSpec.users).
type user struct {
	Name              string   `json:"name"`
	Gecos             string   `json:"gecos,omitempty"`
	Groups            string   `json:"groups,omitempty"`
	HomeDir           string   `json:"homedir,omitempty"`
	Inactive          bool     `json:"inactive,omitempty"`
	Shell             string   `json:"shell,omitempty"`
	Passwd            string   `json:"passwd,omitempty"`
	PrimaryGroup      string   `json:"primary_group,omitempty"`
	LockPassword      *bool    `json:"lock_passwd,omitempty"`
	Sudo              string   `json:"sudo,omitempty"`
	SSHAuthorizedKeys []string `json:"ssh_authorized_keys,omitempty"`
}

// ntp mirrors cloud-init's ntp module as emitted by the kubeadm bootstrap provider.
type ntp struct {
	Enabled *bool    `json:"enabled,omitempty"`
	Servers []string `json:"servers,omitempty"`
}

type writeFile struct {
	Path        string `json:"path"`
	Content     string `json:"content"`
	Encoding    string `json:"encoding,omitempty"`
	Owner       string `json:"owner,omitempty"`
	Permissions string `json:"permissions,omitempty"`
	Append      bool   `json:"append,omitempty"`
}

// command is a cloud-init command: either a shell string or an argv list.
type command []string

func (c *command) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*c = command{s}
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return fmt.Errorf("command must be a string or a list of strings: %w", err)
	}
	*c = command(list)
	return nil
}

// shell renders the command as a bash line.
func (c command) shell() string {
	if len(c) == 1 {
		return c[0]
	}
	quoted := make([]string, 0, len(c))
	for _, arg := range c {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

// Result is the generated startup script plus anything that could not be translated.
type Result struct {
	Script string
	// Unsupported lists cloud-config top-level keys that were present in the
	// input but are not translated (for example users, ntp, mounts).
	Unsupported []string
}

// Placeholders substituted before conversion. Verda has no instance metadata
// service, so the cloud-init jinja variables that Cluster API templates
// conventionally use for the node hostname are resolved here instead.
var hostnamePlaceholders = []string{
	"{{ ds.meta_data.hostname }}",
	"{{ ds.meta_data.local_hostname }}",
	"{{ds.meta_data.hostname}}",
	"{{ds.meta_data.local_hostname}}",
}

// ToStartupScript converts cloud-config bootstrap data into a bash startup script.
// Data that is already a shell script (starts with #!) is returned unchanged.
// hostname replaces the cloud-init hostname placeholders in the data.
func ToStartupScript(data []byte, hostname string) (*Result, error) {
	for _, placeholder := range hostnamePlaceholders {
		data = bytes.ReplaceAll(data, []byte(placeholder), []byte(hostname))
	}
	trimmed := bytes.TrimSpace(data)
	if bytes.HasPrefix(trimmed, []byte("#!")) {
		return &Result{Script: string(data)}, nil
	}
	if !isCloudConfig(trimmed) {
		return nil, fmt.Errorf("bootstrap data is neither a shell script nor %s; ignition and other formats are not supported", cloudConfigHeader)
	}

	var raw map[string]json.RawMessage
	if err := yaml.Unmarshal(trimmed, &raw); err != nil {
		return nil, fmt.Errorf("parsing cloud-config: %w", err)
	}
	var cfg cloudConfig
	if err := yaml.Unmarshal(trimmed, &cfg); err != nil {
		return nil, fmt.Errorf("parsing cloud-config: %w", err)
	}

	res := &Result{}
	for key := range raw {
		if !knownKeys[key] {
			res.Unsupported = append(res.Unsupported, key)
		}
	}
	slices.Sort(res.Unsupported)

	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	b.WriteString("# Generated by cluster-api-provider-verda from Cluster API bootstrap data. Do not edit.\n")
	b.WriteString("set -euo pipefail\n")
	fmt.Fprintf(&b, "exec > >(tee -a %s) 2>&1\n", LogPath)
	b.WriteString("echo \"[capi-bootstrap] starting $(date -Is)\"\n")
	for _, key := range res.Unsupported {
		fmt.Fprintf(&b, "echo \"[capi-bootstrap] warning: cloud-config key %q is not supported on Verda and was ignored\"\n", key)
	}

	if len(cfg.BootCmd) > 0 {
		b.WriteString("\n# bootcmd\n")
		for _, c := range cfg.BootCmd {
			b.WriteString(c.shell())
			b.WriteByte('\n')
		}
	}

	if len(cfg.WriteFiles) > 0 {
		b.WriteString("\n# write_files\n")
		for i, f := range cfg.WriteFiles {
			if err := writeFileScript(&b, i, f); err != nil {
				return nil, err
			}
		}
	}

	if len(cfg.Users) > 0 {
		b.WriteString("\n# users\n")
		for _, u := range cfg.Users {
			userScript(&b, u)
		}
	}

	if cfg.NTP != nil && (cfg.NTP.Enabled == nil || *cfg.NTP.Enabled) {
		b.WriteString("\n# ntp\n")
		ntpScript(&b, cfg.NTP)
	}

	if len(cfg.RunCmd) > 0 {
		b.WriteString("\n# runcmd\n")
		for _, c := range cfg.RunCmd {
			b.WriteString(c.shell())
			b.WriteByte('\n')
		}
	}

	b.WriteString("\necho \"[capi-bootstrap] finished $(date -Is)\"\n")
	script, err := fit(b.String())
	if err != nil {
		return nil, err
	}
	res.Script = script
	return res, nil
}

// fit returns script unchanged if it is small, otherwise wrapped in a
// gzip-decompressing stub, and fails if the result still exceeds MaxScriptSize.
func fit(script string) (string, error) {
	if len(script) <= compressThreshold {
		return script, nil
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if _, err := zw.Write([]byte(script)); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("#!/bin/bash\n")
	b.WriteString("# Generated by cluster-api-provider-verda. The bootstrap script is gzip-compressed below\n")
	b.WriteString("# and unpacked to /run/capi-bootstrap.sh before running.\n")
	b.WriteString("set -euo pipefail\n")
	b.WriteString("base64 -d <<'__CAPI_GZ__' | gunzip > /run/capi-bootstrap.sh\n")
	b.WriteString(wrapBase64(base64.StdEncoding.EncodeToString(buf.Bytes())))
	b.WriteString("__CAPI_GZ__\n")
	b.WriteString("chmod 0700 /run/capi-bootstrap.sh\n")
	b.WriteString("exec /run/capi-bootstrap.sh\n")
	out := b.String()
	if len(out) > MaxScriptSize {
		return "", fmt.Errorf("%w: %d bytes after compression; reduce KubeadmConfig files", ErrScriptTooLarge, len(out))
	}
	return out, nil
}

// userScript creates or updates a user as cloud-init's users module would.
func userScript(b *strings.Builder, u user) {
	if u.Name == "" {
		return
	}
	name := shellQuote(u.Name)
	args := []string{"--create-home"}
	if u.Gecos != "" {
		args = append(args, "--comment", shellQuote(u.Gecos))
	}
	if u.HomeDir != "" {
		args = append(args, "--home-dir", shellQuote(u.HomeDir))
	}
	if u.Shell != "" {
		args = append(args, "--shell", shellQuote(u.Shell))
	}
	if u.PrimaryGroup != "" {
		fmt.Fprintf(b, "getent group %s >/dev/null || groupadd %s\n", shellQuote(u.PrimaryGroup), shellQuote(u.PrimaryGroup))
		args = append(args, "--gid", shellQuote(u.PrimaryGroup))
	}
	if u.Groups != "" {
		args = append(args, "--groups", shellQuote(strings.ReplaceAll(u.Groups, " ", "")))
	}
	if u.Inactive {
		args = append(args, "--expiredate", "1")
	}
	fmt.Fprintf(b, "id -u %s >/dev/null 2>&1 || useradd %s %s\n", name, strings.Join(args, " "), name)
	if u.Passwd != "" {
		fmt.Fprintf(b, "usermod --password %s %s\n", shellQuote(u.Passwd), name)
	}
	if u.LockPassword != nil && *u.LockPassword {
		fmt.Fprintf(b, "passwd -l %s >/dev/null\n", name)
	}
	if u.Sudo != "" {
		fmt.Fprintf(b, "install -d -m 0750 /etc/sudoers.d && echo %s > /etc/sudoers.d/90-capi-%s && chmod 0440 /etc/sudoers.d/90-capi-%s\n",
			shellQuote(u.Name+" "+u.Sudo), u.Name, u.Name)
	}
	if len(u.SSHAuthorizedKeys) > 0 {
		home := fmt.Sprintf("\"$(getent passwd %s | cut -d: -f6)\"", name)
		fmt.Fprintf(b, "install -d -m 0700 -o %s -g %s %s/.ssh\n", name, name, home)
		for _, key := range u.SSHAuthorizedKeys {
			fmt.Fprintf(b, "echo %s >> %s/.ssh/authorized_keys\n", shellQuote(key), home)
		}
		fmt.Fprintf(b, "chown %s:%s %s/.ssh/authorized_keys && chmod 0600 %s/.ssh/authorized_keys\n", name, name, home, home)
	}
}

// ntpScript points chrony (the default on Ubuntu images) at the given servers.
func ntpScript(b *strings.Builder, n *ntp) {
	if len(n.Servers) == 0 {
		return
	}
	b.WriteString("if command -v chronyd >/dev/null 2>&1; then\n")
	b.WriteString("  install -d /etc/chrony/sources.d\n")
	b.WriteString("  : > /etc/chrony/sources.d/capi.sources\n")
	for _, srv := range n.Servers {
		fmt.Fprintf(b, "  echo %s >> /etc/chrony/sources.d/capi.sources\n", shellQuote("server "+srv+" iburst"))
	}
	b.WriteString("  systemctl restart chrony 2>/dev/null || systemctl restart chronyd 2>/dev/null || true\n")
	b.WriteString("fi\n")
}

// isCloudConfig reports whether data carries the #cloud-config header. The
// kubeadm bootstrap provider precedes it with a "## template: jinja" line.
func isCloudConfig(data []byte) bool {
	for _, line := range bytes.SplitN(data, []byte("\n"), 4) {
		if bytes.Equal(bytes.TrimSpace(line), []byte(cloudConfigHeader)) {
			return true
		}
	}
	return false
}

// writeFileScript emits the shell needed to materialise one write_files entry.
// Content is always transported base64-encoded so that arbitrary bytes never
// collide with heredoc delimiters or shell syntax.
func writeFileScript(b *strings.Builder, idx int, f writeFile) error {
	if f.Path == "" {
		return fmt.Errorf("write_files[%d]: path is required", idx)
	}
	content, err := decodeContent(f.Content, f.Encoding)
	if err != nil {
		return fmt.Errorf("write_files[%d] (%s): %w", idx, f.Path, err)
	}
	// gzip content is left compressed and inflated on the instance.
	decoder := "base64 -d"
	if isGzip(f.Encoding) {
		decoder = "base64 -d | gunzip"
	}
	redirect := ">"
	if f.Append {
		redirect = ">>"
	}
	delim := fmt.Sprintf("__CAPI_FILE_%d__", idx)

	fmt.Fprintf(b, "mkdir -p %s\n", shellQuote(path.Dir(f.Path)))
	fmt.Fprintf(b, "%s %s %s <<'%s'\n", decoder, redirect, shellQuote(f.Path), delim)
	b.WriteString(wrapBase64(base64.StdEncoding.EncodeToString(content)))
	fmt.Fprintf(b, "%s\n", delim)
	if f.Permissions != "" {
		fmt.Fprintf(b, "chmod %s %s\n", shellQuote(f.Permissions), shellQuote(f.Path))
	}
	if f.Owner != "" {
		fmt.Fprintf(b, "chown %s %s\n", shellQuote(f.Owner), shellQuote(f.Path))
	}
	return nil
}

// decodeContent returns the raw bytes to ship for a write_files entry. Plain
// and base64 content are decoded; gzip variants are returned still compressed.
func decodeContent(content, encoding string) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "text/plain":
		return []byte(content), nil
	case "b64", "base64", "gz+b64", "gzip+base64", "gz+base64", "gzip+b64":
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(content))
		if err != nil {
			return nil, fmt.Errorf("decoding base64 content: %w", err)
		}
		return decoded, nil
	case "gz", "gzip":
		return []byte(content), nil
	default:
		return nil, fmt.Errorf("unsupported content encoding %q", encoding)
	}
}

func isGzip(encoding string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(encoding)), "gz")
}

// wrapBase64 splits a base64 string into 76-column lines, which base64 -d accepts.
func wrapBase64(s string) string {
	const width = 76
	var b strings.Builder
	for len(s) > width {
		b.WriteString(s[:width])
		b.WriteByte('\n')
		s = s[width:]
	}
	b.WriteString(s)
	b.WriteByte('\n')
	return b.String()
}

// shellQuote single-quotes s for safe use as a bash word.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
