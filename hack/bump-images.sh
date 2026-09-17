#!/usr/bin/env bash
# Point the cluster templates, examples and docs at new node images:
#   hack/bump-images.sh [--node NAME] [--gpu NAME]
# Only the given images are changed. Used by the node-image workflow after a
# build; safe to run by hand.
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
node="" gpu=""
while [ $# -gt 0 ]; do
  case "$1" in
    --node) node="$2"; shift 2 ;;
    --gpu) gpu="$2"; shift 2 ;;
    *) echo "usage: $0 [--node NAME] [--gpu NAME]" >&2; exit 2 ;;
  esac
done
[ -n "$node" ] || [ -n "$gpu" ] || { echo "nothing to bump" >&2; exit 2; }

# sed -i portable between GNU and BSD.
sedi() { if sed --version >/dev/null 2>&1; then sed -i "$@"; else sed -i '' "$@"; fi; }

if [ -n "$node" ]; then
  sedi -E "s#(\\$\\{VERDA_OS_VOLUME_ID:=)[^}]+\\}#\\1${node}}#g" \
    "$root"/templates/cluster-template.yaml "$root"/templates/cluster-template-external-endpoint.yaml
  sedi -E "s#k8s-node-v[0-9.]+-ubuntu-[0-9.]+-[0-9]{8}-[0-9a-f]+#${node}#g" \
    "$root"/templates/cluster-template.yaml "$root"/templates/cluster-template-external-endpoint.yaml \
    "$root"/examples/README.md "$root"/README.md "$root"/docs/node-image.md
fi
if [ -n "$gpu" ]; then
  sedi -E "s#(\\$\\{VERDA_GPU_OS_VOLUME_ID:=)[^}]+\\}#\\1${gpu}}#g" "$root"/examples/gpu-workers/gpu-pool.yaml
  sedi -E "s#k8s-gpu-node-v[0-9.]+-cuda[0-9-]+-[0-9]{8}-[0-9a-f]+#${gpu}#g" \
    "$root"/examples/gpu-workers/gpu-pool.yaml "$root"/examples/README.md
fi
git -C "$root" --no-pager diff --stat -- templates examples README.md docs
