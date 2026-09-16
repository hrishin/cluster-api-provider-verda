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

## Building with image-builder

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
