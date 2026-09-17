# Node images

Machines boot either from a stock Verda image (`VerdaMachine.spec.image`) or
from a clone of a prebuilt OS volume (`spec.osVolumeID`). The OS volume path
is the recommended one: nodes come up in about a minute instead of installing
packages at boot, and every node runs identical bits.

## What the image must contain

The bootstrap data from the kubeadm bootstrap provider is turned into a
startup script (see the README), so the image needs what kubeadm expects:

- `containerd` with the `SystemdCgroup = true` cgroup driver, `runc`, CNI plugins
- `kubeadm`, `kubelet`, `kubectl` for the Kubernetes version the cluster uses
  (the template's `KUBERNETES_VERSION` must match)
- `br_netfilter` loaded and `net.bridge.bridge-nf-call-iptables`,
  `net.ipv4.ip_forward` set to 1; swap disabled
- `bash`, `base64`, `gunzip`, `chrony` (Ubuntu has them) — the startup script
  is bash and ships its payload base64/gzip encoded
- for GPU nodes: the NVIDIA driver, NVIDIA container toolkit, and containerd
  configured with the `nvidia` runtime

The image must **not** have `/run/cluster-api/bootstrap-success.complete` or
leftover kubeadm state (`/etc/kubernetes`, `/var/lib/etcd`) from a previous
run; kubeadm refuses to initialise a node that looks already joined.

Nothing Verda-specific is required: the provider does not depend on
cloud-init (it is fine if it is present) and there is no metadata service to
query.

## Building with Packer (recommended)

[`packer/`](../packer/README.md) builds the node images end to end on Verda
in two stages: the `thevilledev/verda` Packer builder boots a stock Ubuntu
instance, runs image-builder's node roles (`ansible/verda-node.yml`) and
publishes `k8s-node-v<kubernetes>-<source image>-<build id>`; the GPU stage
then clones that volume and layers the NVIDIA driver, CUDA and container
toolkit on it (`ansible/verda-gpu-node.yml`), publishing
`k8s-gpu-node-v<kubernetes>-cuda<version>-<build id>`. All versions are
pinned in `packer/versions.pkrvars.json`.

```sh
# Verda credentials come from VERDA_CLIENT_ID/VERDA_CLIENT_SECRET or ~/.verda/credentials;
# the node SSH key pair from ~/.ssh/verda-k8s-nodes (or NODE_SSH_KEY_FILE).
packer/build.sh validate
packer/build.sh build                          # stage 1, about 10 minutes on a CPU.4V.16G
packer/build.sh build-gpu                      # stage 2 from the newest node image, about 15 minutes
packer/scripts/list-images.sh                  # shows the volumes to use as VERDA_OS_VOLUME_ID
```

Use `k8s-node-*` for control planes and CPU workers and `k8s-gpu-node-*`
for GPU pools (see `examples/gpu-workers`).

The `node-image` GitHub workflow validates on pull requests and builds on
pushes to `main` that touch `packer/`, or on demand; it needs the
`VERDA_CLIENT_ID`, `VERDA_CLIENT_SECRET` and `NODE_SSH_PRIVATE_KEY`
repository secrets.

## Building by hand with image-builder

If you would rather not use Packer,
[kubernetes-sigs/image-builder](https://github.com/kubernetes-sigs/image-builder)
has no Verda target, but its `raw`/`qemu` builders produce a disk image with
the right contents:

```sh
git clone https://github.com/kubernetes-sigs/image-builder && cd image-builder/images/capi
cat > verda.json <<'JSON'
{
  "kubernetes_semver": "v1.35.8",
  "kubernetes_series": "v1.35",
  "kubernetes_deb_version": "1.35.8-1.1",
  "kubernetes_rpm_version": "1.35.8"
}
JSON
PACKER_VAR_FILES=verda.json make build-qemu-ubuntu-2404
```

Then create a Verda OS volume from the result. The Verda API has no image
import endpoint today, so the practical route is:

1. Create a throwaway instance from the stock `ubuntu-24.04` image with an
   OS volume of the desired size.
2. Write the built image onto that instance's root disk from a rescue
   environment, or — simpler — run the image-builder Ansible roles against
   the running instance:
   `ansible-playbook -i <ip>, ansible/node.yml -e @verda.json`
   followed by `kubeadm reset -f`, `cloud-init clean`, and a truncate of
   `/etc/machine-id`.
3. Shut the instance down and delete it **keeping the OS volume**
   (`verda vm delete --keep-volumes`). The detached OS volume is the node
   image; name it e.g. `k8s-node-v1.35.8-ubuntu-24.04-<date>`.

Reference the volume by ID or name in `VERDA_OS_VOLUME_ID`. The provider
clones it per machine (`<namespace>-<machine>-os`) and deletes the clone with
the instance; the source volume is never modified. Verda bills detached
volumes, so keep one per Kubernetes minor you run.

## Stock images

`templates/cluster-template-stock-image.yaml` boots `ubuntu-24.04` and
installs containerd and the Kubernetes packages from `pkgs.k8s.io` in
`preKubeadmCommands`. It works without any image preparation but adds a few
minutes and an apt dependency to every node boot.
