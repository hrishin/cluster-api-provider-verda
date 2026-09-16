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
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// Updater pushes a backend list to a running load balancer.
type Updater interface {
	// UpdateBackends installs the haproxy configuration for backends on the
	// load balancer at addr, authenticating with keys.
	UpdateBackends(ctx context.Context, addr string, keys *Keys, backends []string) error
}

// SSHUpdater implements Updater over SSH as root.
type SSHUpdater struct {
	Timeout time.Duration
}

var _ Updater = &SSHUpdater{}

func (u *SSHUpdater) UpdateBackends(ctx context.Context, addr string, keys *Keys, backends []string) error {
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
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("opening ssh session: %w", err)
	}
	defer session.Close()

	var stderr bytes.Buffer
	session.Stdin = strings.NewReader(Config(backends))
	session.Stderr = &stderr
	if err := session.Run(UpdateCommand); err != nil {
		return fmt.Errorf("updating haproxy backends on %s: %w: %s", addr, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// FakeUpdater records backend updates for tests.
type FakeUpdater struct {
	// Backends is the last backend list pushed per address.
	Backends map[string][]string
	// Calls counts UpdateBackends invocations.
	Calls int
	// Err, when set, is returned by UpdateBackends.
	Err error
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
