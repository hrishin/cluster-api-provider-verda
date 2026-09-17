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
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// File is a configuration file to install on a load balancer.
type File struct {
	Path    string
	Content string
}

// Updater pushes configuration to a running load balancer.
type Updater interface {
	// UpdateBackends installs the haproxy configuration for backends on the
	// control plane load balancer at addr, authenticating with keys.
	UpdateBackends(ctx context.Context, addr string, keys *Keys, backends []string) error
	// UpdateFiles atomically installs files on the load balancer at addr and
	// runs reload afterwards (may be empty).
	UpdateFiles(ctx context.Context, addr string, keys *Keys, files []File, reload string) error
}

// SSHUpdater implements Updater over SSH as root.
type SSHUpdater struct {
	Timeout time.Duration
}

var _ Updater = &SSHUpdater{}

func (u *SSHUpdater) UpdateBackends(ctx context.Context, addr string, keys *Keys, backends []string) error {
	return u.run(ctx, addr, keys, strings.NewReader(Config(backends)), UpdateCommand)
}

// UpdateFiles implements Updater. Each file is written next to its
// destination and moved into place so watchers (envoy) see one atomic change.
func (u *SSHUpdater) UpdateFiles(ctx context.Context, addr string, keys *Keys, files []File, reload string) error {
	return u.run(ctx, addr, keys, strings.NewReader(InstallScript(files, reload)), "bash -s")
}

// InstallScript renders the shell that installs files atomically and reloads.
func InstallScript(files []File, reload string) string {
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	for i, f := range files {
		fmt.Fprintf(&b, "base64 -d > %s.new.%d <<'__CAPI_FILE__'\n%s__CAPI_FILE__\n", f.Path, i, wrapBase64(base64.StdEncoding.EncodeToString([]byte(f.Content))))
		fmt.Fprintf(&b, "mv -f %s.new.%d %s\n", f.Path, i, f.Path)
	}
	if reload != "" {
		b.WriteString(reload + "\n")
	}
	return b.String()
}

// wrapBase64 splits a base64 string into 76-column lines.
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

func (u *SSHUpdater) run(ctx context.Context, addr string, keys *Keys, stdin io.Reader, command string) error {
	signer, err := ssh.ParsePrivateKey(keys.ClientPrivateKey)
	if err != nil {
		return fmt.Errorf("parsing client key: %w", err)
	}
	hostKey, _, _, _, err := ssh.ParseAuthorizedKey(keys.HostPublicKey)
	if err != nil {
		return fmt.Errorf("parsing host key: %w", err)
	}
	timeout := u.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	config := &ssh.ClientConfig{
		User:              "root",
		Auth:              []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback:   ssh.FixedHostKey(hostKey),
		HostKeyAlgorithms: []string{ssh.KeyAlgoED25519},
		Timeout:           timeout,
	}

	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(addr, "22"))
	if err != nil {
		return fmt.Errorf("connecting to load balancer %s: %w", addr, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("ssh handshake with load balancer %s: %w", addr, err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer func() { _ = client.Close() }()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("opening ssh session: %w", err)
	}
	defer func() { _ = session.Close() }()

	var stderr bytes.Buffer
	session.Stdin = stdin
	session.Stderr = &stderr
	if err := session.Run(command); err != nil {
		return fmt.Errorf("updating load balancer %s: %w: %s", addr, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// FakeUpdater records backend updates for tests.
type FakeUpdater struct {
	// Backends is the last backend list pushed per address.
	Backends map[string][]string
	// Files is the last file set pushed per address.
	Files map[string][]File
	// Calls counts UpdateBackends and UpdateFiles invocations.
	Calls int
	// Err, when set, is returned by UpdateBackends and UpdateFiles.
	Err error
}

func (f *FakeUpdater) UpdateFiles(_ context.Context, addr string, _ *Keys, files []File, _ string) error {
	f.Calls++
	if f.Err != nil {
		return f.Err
	}
	if f.Files == nil {
		f.Files = map[string][]File{}
	}
	f.Files[addr] = append([]File(nil), files...)
	return nil
}

var _ Updater = &FakeUpdater{}

func (f *FakeUpdater) UpdateBackends(_ context.Context, addr string, _ *Keys, backends []string) error {
	f.Calls++
	if f.Err != nil {
		return f.Err
	}
	if f.Backends == nil {
		f.Backends = map[string][]string{}
	}
	f.Backends[addr] = append([]string(nil), backends...)
	return nil
}
