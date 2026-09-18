#!/usr/bin/env bash
# Set the release version everywhere it is pinned in the tree, before tagging:
#   hack/bump-version.sh v0.5.0
# Updates the charts' version/appVersion and the cloud controller manager image
# tag in the cluster templates and config/ccm. The release and charts workflows
# refuse a tag whose version differs from the charts.
set -euo pipefail

version="${1:-}"
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$ ]] || {
  echo "usage: $0 vX.Y.Z" >&2
  exit 1
}
here="$(cd "$(dirname "$0")/.." && pwd)"

for chart in "$here"/charts/*/Chart.yaml; do
  sed -i.bak -e "s/^version: .*/version: ${version#v}/" -e "s/^appVersion: .*/appVersion: $version/" "$chart"
  rm -f "$chart.bak"
done

grep -rl 'verda-cloud-controller-manager:v[0-9]' "$here/templates" "$here/config/ccm" | while read -r f; do
  sed -i.bak -E "s#(verda-cloud-controller-manager):v[0-9][^\"' ]*#\1:$version#g" "$f"
  rm -f "$f.bak"
done

git -C "$here" --no-pager diff --stat
