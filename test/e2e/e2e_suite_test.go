//go:build e2e

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

// Suite setup for the end-to-end tests.

package e2e

import (
	"context"
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	infrav1 "github.com/hrishin/verda-capi/api/v1beta1"
	"github.com/hrishin/verda-capi/internal/cloud"
)

var (
	ctx         = context.Background()
	k8sClient   client.Client
	verdaClient cloud.Client
	location    string
	osVolumeID  string
	sshKeyID    string
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "cluster-api-provider-verda e2e")
}

var _ = BeforeSuite(func() {
	for _, v := range []*string{&location, &osVolumeID, &sshKeyID} {
		*v = ""
	}
	location = os.Getenv("E2E_VERDA_LOCATION")
	osVolumeID = os.Getenv("E2E_VERDA_OS_VOLUME_ID")
	sshKeyID = os.Getenv("E2E_VERDA_SSH_KEY_ID")
	if location == "" || osVolumeID == "" || sshKeyID == "" {
		Skip("E2E_VERDA_LOCATION, E2E_VERDA_OS_VOLUME_ID and E2E_VERDA_SSH_KEY_ID must be set")
	}
	if os.Getenv("VERDA_CLIENT_ID") == "" || os.Getenv("VERDA_CLIENT_SECRET") == "" {
		Skip("VERDA_CLIENT_ID and VERDA_CLIENT_SECRET must be set")
	}

	Expect(clusterv1.AddToScheme(scheme.Scheme)).To(Succeed())
	Expect(infrav1.AddToScheme(scheme.Scheme)).To(Succeed())
	var err error
	k8sClient, err = client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())

	creds, err := cloud.LoadCredentials("")
	Expect(err).NotTo(HaveOccurred())
	verdaClient, err = cloud.NewClient(creds)
	Expect(err).NotTo(HaveOccurred())

	SetDefaultEventuallyPollingInterval(15 * time.Second)
})
