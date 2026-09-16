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
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Credentials holds what is needed to talk to the Verda API.
type Credentials struct {
	BaseURL      string
	ClientID     string
	ClientSecret string
}

// Environment variables consulted by LoadCredentials.
const (
	EnvBaseURL      = "VERDA_BASE_URL"
	EnvClientID     = "VERDA_CLIENT_ID"
	EnvClientSecret = "VERDA_CLIENT_SECRET"
	EnvProfile      = "VERDA_PROFILE"
)

// LoadCredentials resolves credentials from the environment first and then
// from an INI-style credentials file (default ~/.verda/credentials, profile
// "default"), the same file used by the verda CLI:
//
//	[default]
//	verda_base_url      = https://api.verda.com/v1
//	verda_client_id     = ...
//	verda_client_secret = ...
func LoadCredentials(path string) (Credentials, error) {
	creds := Credentials{
		BaseURL:      os.Getenv(EnvBaseURL),
		ClientID:     os.Getenv(EnvClientID),
		ClientSecret: os.Getenv(EnvClientSecret),
	}
	if creds.ClientID != "" && creds.ClientSecret != "" {
		return creds, nil
	}

	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return creds, fmt.Errorf("resolving home directory: %w", err)
		}
		path = filepath.Join(home, ".verda", "credentials")
	}
	profile := os.Getenv(EnvProfile)
	if profile == "" {
		profile = "default"
	}

	fileCreds, err := readCredentialsFile(path, profile)
	if err != nil {
		return creds, err
	}
	if creds.BaseURL == "" {
		creds.BaseURL = fileCreds.BaseURL
	}
	if creds.ClientID == "" {
		creds.ClientID = fileCreds.ClientID
	}
	if creds.ClientSecret == "" {
		creds.ClientSecret = fileCreds.ClientSecret
	}
	if creds.ClientID == "" || creds.ClientSecret == "" {
		return creds, fmt.Errorf("no Verda credentials: set %s and %s or add them to profile %q in %s", EnvClientID, EnvClientSecret, profile, path)
	}
	return creds, nil
}

func readCredentialsFile(path, profile string) (Credentials, error) {
	var creds Credentials
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return creds, nil
		}
		return creds, fmt.Errorf("opening credentials file: %w", err)
	}
	defer f.Close()

	inProfile := false
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inProfile = strings.TrimSpace(line[1:len(line)-1]) == profile
			continue
		}
		if !inProfile {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "verda_base_url":
			creds.BaseURL = value
		case "verda_client_id":
			creds.ClientID = value
		case "verda_client_secret":
			creds.ClientSecret = value
		}
	}
	if err := scanner.Err(); err != nil {
		return creds, fmt.Errorf("reading credentials file: %w", err)
	}
	return creds, nil
}
