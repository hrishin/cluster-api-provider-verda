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

// Entry point of the Verda cloud controller manager that runs inside workload clusters.

package main

import (
	"os"

	"k8s.io/apimachinery/pkg/util/wait"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/cloud-provider/app"
	"k8s.io/cloud-provider/app/config"
	"k8s.io/cloud-provider/names"
	"k8s.io/cloud-provider/options"
	"k8s.io/component-base/cli"
	cliflag "k8s.io/component-base/cli/flag"
	_ "k8s.io/component-base/metrics/prometheus/clientgo"
	_ "k8s.io/component-base/metrics/prometheus/version"
	"k8s.io/klog/v2"

	"github.com/hrishin/verda-capi/internal/ccm"
)

func main() {
	ccmOptions, err := options.NewCloudControllerManagerOptions()
	if err != nil {
		klog.ErrorS(err, "Unable to initialize command options")
		os.Exit(1)
	}
	ccmOptions.KubeCloudShared.CloudProvider.Name = ccm.ProviderName

	controllerInitializers := app.DefaultInitFuncConstructors

	delete(controllerInitializers, names.NodeRouteController)
	for name, constructor := range controllerInitializers {
		constructor.InitContext.ClientName = "verda-external-" + name
		controllerInitializers[name] = constructor
	}

	command := app.NewCloudControllerManagerCommand(
		ccmOptions, cloudInitializer, controllerInitializers, names.CCMControllerAliases(),
		cliflag.NamedFlagSets{}, wait.NeverStop,
	)
	os.Exit(cli.Run(command))
}

func cloudInitializer(completed *config.CompletedConfig) cloudprovider.Interface {
	cloudConfig := completed.ComponentConfig.KubeCloudShared.CloudProvider
	cloud, err := cloudprovider.InitCloudProvider(cloudConfig.Name, cloudConfig.CloudConfigFile)
	if err != nil {
		klog.ErrorS(err, "Cloud provider could not be initialized")
		os.Exit(1)
	}
	if cloud == nil {
		klog.ErrorS(nil, "Cloud provider is nil")
		os.Exit(1)
	}
	return cloud
}
