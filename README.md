# Cluster API Provider Verda (CAPV)

A [Cluster API](https://cluster-api.sigs.k8s.io/) infrastructure provider for
[Verda Cloud](https://verda.com) (formerly DataCrunch). It lets Cluster API
create kubeadm-based Kubernetes clusters on Verda instances.

Implements the Cluster API **v1beta2 provider contract** (Cluster API v1.14+).

## What it does

| Resource | Purpose |
|---|---|
| `VerdaCluster` | Cluster-level infrastructure. Verda has no managed network or load balancer, so the controller either surfaces a user-provided control plane endpoint or provisions an haproxy instance in front of the control plane (`spec.controlPlaneLoadBalancer`, kept in sync with the control-plane machines), optionally an envoy instance for `Service type=LoadBalancer` (`spec.serviceLoadBalancer`), and publishes the location as the failure domain. Both instances are deleted with the cluster. |
| `VerdaMachine` | One Verda instance. Turns the kubeadm bootstrap data into a Verda startup script, creates the instance, and reports its state, address and provider ID. |
| `VerdaMachineTemplate` | Template for `MachineDeployment` / `KubeadmControlPlane`. |
| `VerdaClusterTemplate` | Template for ClusterClass. |
| `verda-cloud-controller-manager` | Runs inside workload clusters (kubelets use `--cloud-provider=external`): sets node addresses, provider ID and zone, removes Nodes whose instance is gone, and implements `Service type=LoadBalancer` on the service load balancer. Installed by the cluster templates through a ClusterResourceSet. |

### Design notes specific to Verda

- **Control plane endpoint is user-provided.** Verda offers no load balancer or
  floating IPs, so `VerdaCluster.spec.controlPlaneEndpoint` must be a DNS name
  or IP you control that routes to the control plane machine(s).
- **Services of type LoadBalancer.** With `VerdaCluster.spec.serviceLoadBalancer.enabled`
  the provider runs an envoy instance per cluster and hands its address and
  SSH keys to the workload cluster (`kube-system/verda-service-lb`); the cloud
  controller manager renders one envoy listener per Service port (TCP and UDP,
  layer 4 pass-through — TLS terminates in the cluster) forwarding to the
  NodePorts of Ready workers, with active health checks and
  `externalTrafficPolicy: Local` support. All Services share the instance's IP,
  so two Services cannot use the same port. Annotate a Service with
  `verda.cluster.x-k8s.io/proxy-protocol: "true"` to get PROXY protocol v2
  upstream (e.g. for ingress-nginx `use-proxy-protocol`). Setting `enabled`
  back to `false` deletes the envoy instance and its volume and removes the
  Secret from the workload cluster; existing LoadBalancer Services lose their
  external IP.
- **Boot from an image-builder OS volume.** `VerdaMachine.spec.osVolumeID`
  points at a detached OS volume by ID or exact name (e.g. built with
  image-builder, containing kubeadm/kubelet/containerd). The provider clones it per machine
  (`<namespace>-<machine>-os`), boots the instance from the clone and deletes
  the clone with the instance. `spec.image` boots from a stock Verda image instead.
- **Bootstrap data is delivered as a startup script.** Verda instances take a
  shell script at boot, not cloud-init user-data. The provider converts the
  `#cloud-config` produced by the kubeadm bootstrap provider (`bootcmd`,
  `write_files`, `users`, `ntp`, `runcmd`) into bash (`internal/bootstrap`).
  Unsupported sections (`mounts`, `disk_setup`, …) are logged and skipped.
  Ignition is not supported. Verda accepts scripts up to 48 KiB, so scripts
  over 24 KiB are shipped gzip-compressed; a script that still does not fit
  fails the machine with a clear error.
- **Provider ID is `verda://<hostname>`**, not the instance ID: the startup
  script has to exist before the instance does, and there is no metadata
  service on the node. Templates use
  `--provider-id=verda://{{ ds.meta_data.hostname }}`; the placeholder is
  resolved by the provider.
- **Data volumes and spot policy.** `spec.additionalVolumes` creates data
  volumes with the instance (deleted with it; format/mount them via
  KubeadmConfig `diskSetup`/`mounts` or `preKubeadmCommands`);
  `spec.spot` + `spec.spotDiscontinuePolicy` control spot behaviour.
- **Instances and clones are tagged** (`capi-cluster`, `capi-machine`, `capi-managed-by`)
  so a create whose result was never persisted is recovered rather than duplicated,
  and the client refuses to delete anything not carrying `capi-managed-by`.

## Layout

```
api/v1beta1/            CRD types
cmd/                    manager (cmd/main.go) and cloud-controller-manager
internal/ccm/           Verda cloud provider for the cloud controller manager
internal/controller/    VerdaCluster, VerdaMachine and VerdaMachineTemplate reconcilers
internal/cloud/         Thin interface over the Verda SDK + in-memory fake for tests
internal/bootstrap/     cloud-config -> startup script converter
config/                 kustomize (CRDs, RBAC, manager)
templates/              clusterctl cluster templates (flavors)
metadata.yaml           clusterctl contract mapping
```

## Credentials

The manager reads credentials from, in order:

1. `VERDA_CLIENT_ID`, `VERDA_CLIENT_SECRET` (and optionally `VERDA_BASE_URL`)
2. An INI file, default `~/.verda/credentials` (`--credentials-file` to override,
   `VERDA_PROFILE` to select a profile other than `default`) — the same file the
   `verda` CLI uses.

In-cluster, create a Secret named `verda-credentials` in the provider namespace
with keys `client-id`, `client-secret` (and optionally `base-url`); the
deployment maps them to the environment variables above.

Per-cluster credentials (multi-tenancy): set `VerdaCluster.spec.identityRef.name`
to a Secret in the cluster's namespace with the same keys. Clusters without an
`identityRef` use the manager's global credentials; if the manager has none,
every cluster must set one. `--namespace` restricts the manager to one namespace.

## Development

```sh
make test                    # envtest-based unit tests (fake Verda client)
make install                 # install CRDs into the current kubeconfig context
make run                     # run the manager locally against that cluster
make docker-build-ccm docker-push-ccm CCM_IMG=docker.io/you/verda-cloud-controller-manager:v0.1.0
make release-manifests IMG=ghcr.io/you/cluster-api-provider-verda:v0.1.0
                             # out/infrastructure-components.yaml, metadata.yaml, cluster-template.yaml
```

### Install with Helm

Two charts are published as OCI artifacts on Docker Hub (see `charts/`):
`cluster-api-provider-verda` (manager, webhooks, RBAC and by default the CRDs)
and `cluster-api-provider-verda-crds` (CRDs only, for CRD lifecycle managed
separately; then install the main chart with `--set crds.install=false`).

```sh
helm install capv oci://registry-1.docker.io/hriships/cluster-api-provider-verda \
  --version <version> -n capv-system --create-namespace \
  --set credentials.create=true --set credentials.clientId=$VERDA_CLIENT_ID --set credentials.clientSecret=$VERDA_CLIENT_SECRET
```

Cluster API core and the kubeadm providers must already be installed
(`clusterctl init --core cluster-api --bootstrap kubeadm --control-plane kubeadm`).
Optional values: `metrics.enabled` + `metrics.serviceMonitor.enabled` for Prometheus.

### Install with clusterctl

Releases publish `infrastructure-components.yaml`, `metadata.yaml` and the
cluster templates, and images to `docker.io/hriships`. Register the provider
in `~/.config/cluster-api/clusterctl.yaml`:

```yaml
providers:
  - name: verda
    type: InfrastructureProvider
    url: https://github.com/hrishin/cluster-api-provider-verda/releases/latest/infrastructure-components.yaml
```

then, with `VERDA_CLIENT_ID`/`VERDA_CLIENT_SECRET` exported (clusterctl turns
them into the provider's credentials Secret):

```sh
clusterctl init --infrastructure verda
clusterctl generate cluster demo --infrastructure verda --flavor stock-image ...
```

### Try it from source on a kind management cluster

```sh
kind create cluster --name seed
clusterctl init --core cluster-api --bootstrap kubeadm --control-plane kubeadm
make install
make run &

export CLUSTER_NAME=demo KUBERNETES_VERSION=v1.35.8
export CONTROL_PLANE_ENDPOINT_HOST=demo.example.com   # DNS name you control
export VERDA_LOCATION=FIN-03 VERDA_SSH_KEY_ID=<ssh key id>
# VERDA_OS_VOLUME_ID defaults to the current packer/ node image (k8s-node-v1.35.8-ubuntu-24.04-20260917-5a5059e)
clusterctl generate cluster $CLUSTER_NAME --from templates/cluster-template.yaml | kubectl apply -f -
```

Flavors: the default template boots from `VERDA_OS_VOLUME_ID`;
`--flavor stock-image` (`templates/cluster-template-stock-image.yaml`) boots a
stock Ubuntu image and installs kubeadm with apt at first boot.

Watch progress with `kubectl get cluster,verdacluster,machines,verdamachines`.
The control plane template maps `CONTROL_PLANE_ENDPOINT_HOST` to the node's own
IP in `/etc/hosts`, so a single control plane node initialises before DNS exists.
After that, `CONTROL_PLANE_ENDPOINT_HOST` must resolve to the control plane IP
(`kubectl get verdamachine -o wide`) from the management cluster (for KCP to
manage it) and from worker nodes (to join). With a real DNS record that's
automatic; without one, add it to the management cluster's CoreDNS `hosts`
block and to the worker `KubeadmConfigTemplate.preKubeadmCommands`.

## Further reading

- [docs/node-image.md](docs/node-image.md) — what a node image needs; [packer/](packer/README.md) builds it
- [docs/api-versioning.md](docs/api-versioning.md) — API stability and how a new version would be added
- [examples/](examples/) — HA cluster, autoscaling, GPU workers

## Status / not yet done

- `VerdaMachinePool`: Verda has no autoscaling-group equivalent, so machine
  pools are not planned; use MachineDeployments with cluster-autoscaler.
- The service load balancer is a single instance per cluster (no HA), TCP/UDP
  only (no SCTP), and does not terminate TLS.
- Only one API version (`v1beta1`); see [docs/api-versioning.md](docs/api-versioning.md).

Verified live (2026-09): a single control plane + one worker in FIN-03 booted
from an image-builder OS volume, with `Machine`s reaching `Running` and
`nodeRef` set (provider ID matching works).
