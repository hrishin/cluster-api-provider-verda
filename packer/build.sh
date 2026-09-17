#!/usr/bin/env bash
# Build the Kubernetes node image (a Verda OS volume) with Packer.
#
#   packer/build.sh validate                # no Verda resources touched
#   packer/build.sh build [packer args]     # stage 1: the node image, e.g. -var location=FIN-02
#   packer/build.sh build-gpu [packer args] # stage 2: the GPU image from the node image in
#                                           # $NODE_IMAGE (volume ID or exact name; default: the
#                                           # newest k8s-node-* volume in the build location)
#
# Verda API credentials: VERDA_CLIENT_ID / VERDA_CLIENT_SECRET, else the verda
# CLI's ~/.verda/credentials (scripts/verda-env.sh). The node SSH key is the
# key pair in $NODE_SSH_KEY_FILE: its public half is registered in the Verda
# project as "k8s-node" and baked into the node image, and the GPU stage logs
# in to the node image clone with the private half.
# Env: NODE_SSH_KEY_FILE  private key of the node key pair (default ~/.ssh/verda-k8s-nodes)
#      SSH_KEY_IDS        Verda key IDs to bake instead ("none" for none; stage 1 only)
#      BUILD_ID           volume-name suffix (default: <date>-<git sha>)
#      NODE_IMAGE         build-gpu: the node image to start from (ID or name)
#      LOCATION           build-gpu: where to clone and build (default FIN-03)
#      GPU_INSTANCE_TYPE  build-gpu: GPU instance type to build and verify on (default 1RTXPRO6000.30V)
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$here"
NODE_SSH_KEY_FILE="${NODE_SSH_KEY_FILE:-$HOME/.ssh/verda-k8s-nodes}"

action="${1:-validate}"
shift || true

PACKER="${PACKER:-packer}"
VERSIONS="$here/versions.pkrvars.json"
IB_DIR="$here/.image-builder"
VENV="$here/.venv"
BASE_URL="${VERDA_BASE_URL:-https://api.verda.com/v1}"

image_builder_ref="$(jq -r .image_builder_ref "$VERSIONS")"
ansible_core_version="$(jq -r .ansible_core_version "$VERSIONS")"

log() { printf '\033[1;34m==> %s\033[0m\n' "$*" >&2; }

ensure_image_builder() {
  if [ -d "$IB_DIR/.git" ] && [ "$(git -C "$IB_DIR" describe --tags --exact-match 2>/dev/null || true)" = "$image_builder_ref" ]; then
    return
  fi
  log "checking out kubernetes-sigs/image-builder@$image_builder_ref"
  rm -rf "$IB_DIR"
  git clone --quiet --depth 1 --branch "$image_builder_ref" https://github.com/kubernetes-sigs/image-builder "$IB_DIR"
}

ensure_ansible() {
  if ! "$VENV/bin/ansible-playbook" --version 2>/dev/null | head -1 | grep -q "core $ansible_core_version"; then
    log "installing ansible-core $ansible_core_version into $VENV"
    python3 -m venv "$VENV"
    "$VENV/bin/pip" install --quiet --disable-pip-version-check "ansible-core==$ansible_core_version"
  fi
  export PATH="$VENV/bin:$PATH"
  if ! ansible-galaxy collection list 2>/dev/null | grep -qE '^ansible\.posix '; then
    log "installing ansible collections"
    ansible-galaxy collection install -r "$here/requirements.yml"
  fi
}

verda_token() {
  curl -fsS -X POST "$BASE_URL/oauth2/token" -H 'Content-Type: application/json' \
    -d "$(jq -n --arg id "$VERDA_CLIENT_ID" --arg secret "$VERDA_CLIENT_SECRET" \
      '{grant_type: "client_credentials", client_id: $id, client_secret: $secret}')" \
    | jq -r '.access_token'
}

# The node key's public half, derived from the private key file.
node_public_key() {
  [ -f "$NODE_SSH_KEY_FILE" ] || {
    echo "node SSH key not found: $NODE_SSH_KEY_FILE (set NODE_SSH_KEY_FILE, or create one: ssh-keygen -t ed25519 -f ~/.ssh/verda-k8s-nodes)" >&2
    exit 1
  }
  ssh-keygen -y -f "$NODE_SSH_KEY_FILE" | awk 'NF {print $1, $2; exit}'
}

# The node key, registered in the Verda project (by key material; created as
# "k8s-node" if it isn't there yet) so the builder injects it — Verda only
# takes key IDs.
ssh_key_ids_json() {
  local ids="${SSH_KEY_IDS:-}" pub tok id
  if [ "$ids" = "none" ]; then
    echo '[]'
    return
  fi
  if [ -z "$ids" ]; then
    pub="$(node_public_key)"
    tok="$(verda_token)"
    id="$(curl -fsS "$BASE_URL/sshkeys" -H "Authorization: Bearer $tok" \
      | jq -r --arg pub "$pub" '[.[] | select((.key | split(" ") | .[0:2] | join(" ")) == $pub)] | .[0].id // ""')"
    if [ -z "$id" ]; then
      log "registering the cluster SSH key in the Verda project as k8s-node"
      id="$(curl -fsS -X POST "$BASE_URL/sshkeys" -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
        -d "$(jq -n --arg key "$pub" '{name: "k8s-node", key: $key}')" | jq -r '.id // .')"
    fi
    log "baking the cluster SSH key ($id)"
    ids="$id"
  fi
  jq -cn --arg ids "$ids" '$ids | split(",") | map(select(length > 0))'
}

build_id() {
  echo "${BUILD_ID:-$(date -u +%Y%m%d)-$(git -C "$here" rev-parse --short HEAD 2>/dev/null || echo local)}"
}

api() { # api <method> <path> [json body]
  local method="$1" path="$2" body="${3:-}"
  if [ -n "$body" ]; then
    curl -fsS -X "$method" "$BASE_URL$path" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "$body"
  else
    curl -fsS -X "$method" "$BASE_URL$path" -H "Authorization: Bearer $TOKEN"
  fi
}

# The node image to start the GPU stage from: $NODE_IMAGE as an ID or an exact
# name, else the newest detached k8s-node-* volume in the location.
resolve_node_image() {
  local location="$1" volumes
  volumes="$(api GET /volumes)"
  if [ -n "${NODE_IMAGE:-}" ]; then
    jq -r --arg q "$NODE_IMAGE" '[.[] | select(.id == $q or .name == $q)] | .[0] | select(. != null) | "\(.id) \(.name) \(.ssh_key_ids | join(","))"' <<<"$volumes"
  else
    jq -r --arg loc "$location" '[.[] | select(.is_os_volume and .status == "detached" and .location == $loc and (.name | startswith("k8s-node-")))]
      | sort_by(.created_at) | last | select(. != null) | "\(.id) \(.name) \(.ssh_key_ids | join(","))"' <<<"$volumes"
  fi
}

# Clone the node image so the build never touches it; prints the clone ID once
# it is detached (usable).
clone_node_image() {
  local source="$1" name="$2" location="$3" id status
  id="$(api PUT /volumes "$(jq -n --arg id "$source" --arg name "$name" --arg loc "$location" \
    '{action: "clone", id: $id, name: $name, location_code: $loc}')" | jq -r 'if type == "array" then .[0] else . end')"
  [ -n "$id" ] && [ "$id" != "null" ] || {
    echo "cloning $source failed" >&2
    exit 1
  }
  for _ in $(seq 1 60); do
    status="$(api GET "/volumes/$id" | jq -r .status)"
    [ "$status" = "detached" ] && break
    sleep 10
  done
  [ "$status" = "detached" ] || {
    echo "clone $id is still $status after 10 minutes" >&2
    exit 1
  }
  echo "$id"
}

# The builder deletes the build instance's OS volume (the clone) with the
# instance; this only catches a clone left behind by an interrupted build.
delete_volume_if_present() {
  local id="$1" status
  status="$(api GET "/volumes/$id" 2>/dev/null | jq -r '.status // empty')"
  case "$status" in
    "" | deleted) ;;
    *)
      log "removing build clone $id ($status)"
      api DELETE "/volumes/$id" '{"is_permanent": true}' >/dev/null 2>&1 || true
      ;;
  esac
}

case "$action" in
  validate)
    ensure_image_builder
    ensure_ansible
    "$PACKER" init .
    log "ansible syntax check"
    ANSIBLE_ROLES_PATH="$IB_DIR/images/capi/ansible/roles" \
      ansible-playbook --syntax-check -i 'localhost,' "$here/ansible/verda-node.yml" >/dev/null
    ANSIBLE_ROLES_PATH="$IB_DIR/images/capi/ansible/roles" \
      ansible-playbook --syntax-check -i 'localhost,' "$here/ansible/verda-gpu-node.yml" >/dev/null
    log "packer validate"
    VERDA_CLIENT_ID="${VERDA_CLIENT_ID:-validate}" VERDA_CLIENT_SECRET="${VERDA_CLIENT_SECRET:-validate}" \
      "$PACKER" validate -var-file="$VERSIONS" -var "image_builder_dir=$IB_DIR" "$@" .
    # The GPU stage needs a parseable private key even to validate.
    keydir="$(mktemp -d)"
    ssh-keygen -q -t ed25519 -N "" -f "$keydir/id" >/dev/null
    VERDA_CLIENT_ID="${VERDA_CLIENT_ID:-validate}" VERDA_CLIENT_SECRET="${VERDA_CLIENT_SECRET:-validate}" \
      "$PACKER" validate -var-file="$VERSIONS" -var "image_builder_dir=$IB_DIR" \
        -var gpu=true -var source_image=volume-id -var 'ssh_key_ids=["key-id"]' -var "ssh_private_key_file=$keydir/id" "$@" .
    rm -rf "$keydir"
    ;;
  build)
    eval "$("$here/../scripts/verda-env.sh")"
    ensure_image_builder
    ensure_ansible
    "$PACKER" init .
    keys="$(ssh_key_ids_json)"
    id="$(build_id)"
    log "packer build (build_id=$id)"
    "$PACKER" build -timestamp-ui \
      -var-file="$VERSIONS" \
      -var "image_builder_dir=$IB_DIR" \
      -var "ssh_key_ids=$keys" \
      -var "build_id=$id" \
      "$@" .
    log "artifact: $(jq -r '.builds[-1] | "\(.custom_data.image_name) → \(.artifact_id) (\(.custom_data.location))"' "$here/packer-manifest.json")"
    ;;
  build-gpu)
    eval "$("$here/../scripts/verda-env.sh")"
    ensure_image_builder
    ensure_ansible
    "$PACKER" init .
    location="${LOCATION:-FIN-03}"
    TOKEN="$(verda_token)"
    read -r node_id node_name node_keys <<<"$(resolve_node_image "$location")"
    [ -n "${node_id:-}" ] || {
      echo "no node image found (set NODE_IMAGE to a k8s-node-* volume ID or name, or run 'build' first)" >&2
      exit 1
    }
    id="$(build_id)"
    # Verda cannot inject keys into a volume-booted instance: log in with the
    # private half of the key stage 1 baked in. Packer needs a file it can
    # read; copy it into a private temp dir for the duration of the build.
    node_public_key >/dev/null
    keydir="$(mktemp -d)"
    cp "$NODE_SSH_KEY_FILE" "$keydir/id"
    chmod 600 "$keydir/id"
    log "cloning node image $node_name ($node_id) for the GPU stage"
    clone="$(clone_node_image "$node_id" "packer-k8s-gpu-node-$id-build" "$location")"
    # Tokens live ten minutes and the build takes about that long: refresh
    # before cleaning up, and never let cleanup change the build's exit status.
    cleanup_gpu_build() {
      local rc=$?
      { TOKEN="$(verda_token)" && delete_volume_if_present "$clone"; } || true
      rm -rf "$keydir"
      exit "$rc"
    }
    trap cleanup_gpu_build EXIT
    log "packer build gpu (build_id=$id, from $node_name)"
    # The clone inherits the image's keys; Verda accepts those (and no others)
    # on a volume boot, and the builder wants a non-empty list. Keys that no
    # longer exist (stage 1's temporary Packer key) make the API answer
    # "volume not found", so keep only the ones still in the project.
    keys="$(api GET /sshkeys | jq -c --arg ids "${node_keys:-}" '[.[].id] as $existing | $ids | split(",") | map(select(length > 0 and IN($existing[])))')"
    [ "$keys" != "[]" ] || {
      echo "none of the node image's SSH keys ($node_keys) exist in the project any more" >&2
      exit 1
    }
    "$PACKER" build -timestamp-ui \
      -var-file="$VERSIONS" \
      -var "image_builder_dir=$IB_DIR" \
      -var "ssh_key_ids=$keys" \
      -var "ssh_private_key_file=$keydir/id" \
      -var "build_id=$id" \
      -var "gpu=true" \
      -var "gpu_instance_type=${GPU_INSTANCE_TYPE:-1RTXPRO6000.30V}" \
      -var "location=$location" \
      -var "source_image=$clone" \
      -var "source_node_image=$node_name" \
      "$@" .
    log "artifact: $(jq -r '.builds[-1] | "\(.custom_data.image_name) → \(.artifact_id) (\(.custom_data.location))"' "$here/packer-manifest.json")"
    ;;
  *)
    echo "usage: $0 {validate|build|build-gpu} [packer args...]" >&2
    exit 2
    ;;
esac
