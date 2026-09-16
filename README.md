# Cluster API Provider Verda (CAPV)

A [Cluster API](https://cluster-api.sigs.k8s.io/) infrastructure provider for
[Verda Cloud](https://verda.com) (formerly DataCrunch). It lets Cluster API
create kubeadm-based Kubernetes clusters on Verda instances.

Implements the Cluster API **v1beta2 provider contract** (Cluster API v1.14+).

## What it does

| Resource | Purpose |
|---|---|
| `VerdaCluster` | Cluster-level infrastructure. Verda has no managed network or load balancer, so this is limited to validating and surfacing the control plane endpoint. |
| `VerdaMachine` | One Verda instance. Turns the kubeadm bootstrap data into a Verda startup script, creates the instance, and reports its state, address and provider ID. |
| `VerdaMachineTemplate` | Template for `MachineDeployment` / `KubeadmControlPlane`. |
| `VerdaClusterTemplate` | Template for ClusterClass. |

### Design notes specific to Verda

- **Control plane endpoint is user-provided.** Verda offers no load balancer or
  floating IPs, so `VerdaCluster.spec.controlPlaneEndpoint` must be a DNS name
  or IP you control that routes to the control plane machine(s).
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
internal/controller/    VerdaCluster and VerdaMachine reconcilers
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
make release-manifests IMG=ghcr.io/you/cluster-api-provider-verda:v0.1.0
                             # out/infrastructure-components.yaml, metadata.yaml, cluster-template.yaml
```

### Try it on a kind management cluster

```sh
kind create cluster --name seed
clusterctl init --core cluster-api --bootstrap kubeadm --control-plane kubeadm
make install
make run &

export CLUSTER_NAME=demo KUBERNETES_VERSION=v1.35.8
export CONTROL_PLANE_ENDPOINT_HOST=demo.example.com   # DNS name you control
export VERDA_LOCATION=FIN-03 VERDA_SSH_KEY_ID=<ssh key id>
export VERDA_OS_VOLUME_ID=<detached OS volume built with image-builder>
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

## Status / not yet done

- Validation and defaulting webhooks (immutability of `VerdaMachine` spec,
  SSA dry-run support on templates for ClusterClass).
- `VerdaMachineTemplate.status.capacity` for cluster-autoscaler scale-from-zero.
- Multi-tenancy via a per-cluster identity reference; credentials are currently global.
- A Verda cloud-controller-manager.
- E2E tests against a real Verda account (`test/e2e` is the kubebuilder scaffold).
- Load balancer / HA control plane: with no Verda LB, an HA control plane needs
  external DNS or a self-managed LB instance (a haproxy instance managed by
  `VerdaCluster`, CAPD-style, would remove the DNS requirement entirely).

Verified live (2026-09): a single control plane + one worker in FIN-03 booted
from an image-builder OS volume, with `Machine`s reaching `Running` and
`nodeRef` set (provider ID matching works).
