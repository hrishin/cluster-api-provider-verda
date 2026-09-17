# Kubernetes node images for Verda

Two images, built in two stages, each a Verda OS volume:

1. **`k8s-node-*`** — a Kubernetes 1.35 node (containerd, runc,
   kubelet/kubeadm/kubectl, CNI plugins, crictl) built from stock `ubuntu-24.04`
   with `ansible/verda-node.yml` (kubernetes-sigs/image-builder's node roles).
2. **`k8s-gpu-node-*`** — a clone of the node image plus the NVIDIA open kernel
   modules and driver, the CUDA toolkit and the NVIDIA container toolkit
   (`ansible/verda-gpu-node.yml`), with `nvidia` as containerd's default runtime.
   Nodes booted from it need only the NVIDIA device plugin (or the GPU operator
   with `driver.enabled=false` and `toolkit.enabled=false`). The stage builds on
   a GPU instance — the first type in `GPU_INSTANCE_TYPES` (default
   `1RTXPRO6000.30V,1RTXPRO6000.30V.CC,2RTXPRO6000.60V,1A100.22V`) that has
   capacity, waiting up to `GPU_CAPACITY_WAIT` minutes for one — and verifies the
   driver and the container runtime against the card before capturing; the GPU
   image is always derived from a node image, never from a different base.

Verda has no image import; a **detached OS volume** is its custom image
(`POST /v1/instances` takes a volume ID as `image`). The
[`thevilledev/verda`](https://github.com/thevilledev/packer-plugin-verda) builder boots a
stock Ubuntu instance, runs the playbook over SSH, shuts it down, clones the OS volume as
`k8s-node-v<kubernetes>-<source image>-<build id>` and deletes the build instance. Every
cluster node then boots from its own clone of that volume.

| File | Purpose |
|---|---|
| `verda-k8s-node.pkr.hcl`, `variables.pkr.hcl` | Template |
| `versions.pkrvars.json` | Pinned Kubernetes / containerd / runc / crictl / CNI / image-builder / ansible-core versions |
| `ansible/verda-node.yml` | Stage 1, the node build |
| `ansible/verda-gpu-node.yml` | Stage 2, the NVIDIA stack on a node image clone |
| `ansible/sysprep.yml` | Shared last play of both stages: a conservative sysprep (image-builder's own would remove netplan and authorized_keys) |
| `build.sh` | image-builder checkout, ansible venv, SSH-key discovery, `validate` / `build` (stage 1) / `build-gpu` (stage 2: clones the node image, builds, removes the clone) |
| `scripts/list-images.sh` | Lists published images |

## Build

Needs `packer` ≥ 1.11, `python3` ≥ 3.11, `git`, `jq`, `curl`.

```bash
# Verda credentials: VERDA_CLIENT_ID/VERDA_CLIENT_SECRET or ~/.verda/credentials.
# Node SSH key pair (baked into the images): ~/.ssh/verda-k8s-nodes or $NODE_SSH_KEY_FILE.
packer/build.sh validate
packer/build.sh build               # stage 1, ≈10 min on a CPU.4V.16G
packer/build.sh build-gpu           # stage 2 from the newest k8s-node-* (or NODE_IMAGE=<id|name>), ≈10 min on a 1RTXPRO6000.30V
packer/scripts/list-images.sh
```

`build.sh build` takes the Verda credentials from `VERDA_CLIENT_ID`/`VERDA_CLIENT_SECRET`
or `~/.verda/credentials` and bakes the public half of the node key pair
(`~/.ssh/verda-k8s-nodes`, or `NODE_SSH_KEY_FILE`) into the image (registering it in the
project as `k8s-node` if it isn't yet; `SSH_KEY_IDS=id1,id2` to bake others instead):
Verda injects no keys into a volume-booted instance,
so these are the only way in. Rebuild after rotating keys.

The `node-image` workflow does the same on every push to `main` touching `packer/`, or
on demand (location, instance type, source image, extra locations, spot). Pull requests
only validate. It needs three repository secrets: `VERDA_CLIENT_ID` and
`VERDA_CLIENT_SECRET` (the project the images are published in) and
`NODE_SSH_PRIVATE_KEY` (the node key pair's private half). The volume ID ends up in
`packer-manifest.json` (`artifact_id`), and after a successful build the workflow
opens a pull request (`hack/bump-images.sh`) pointing the cluster templates and
examples at the new images. Being opened with the workflow token, that pull
request does not trigger the other workflows; merge it after a look.

## Cost

The node stage runs one `CPU.4V.16G` for ~10 minutes, the GPU stage one
`1RTXPRO6000.30V` for ~10 minutes; each leaves a 50 GB volume billed until deleted.
Old images are not pruned: `verda volume delete <id>`.

## Use

Set `node_image` in `clusters/<name>/config.yaml` to the image name and
`terraform apply`; the cluster module clones it once per node. For GPU nodes build from a
`*-cuda-*` `source_image` and set it as that group's `image`.
