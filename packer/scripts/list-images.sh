#!/usr/bin/env bash
# List node images (detached OS volumes named k8s-node-*), newest first.
#   packer/scripts/list-images.sh [--prefix NAME_PREFIX] [-o table|json]
# Delete one with: verda volume delete <id>
set -euo pipefail

# Credentials from the environment or ~/.verda/credentials.
eval "$("$(dirname "${BASH_SOURCE[0]}")/../../scripts/verda-env.sh")"
BASE_URL="${VERDA_BASE_URL:-https://api.verda.com/v1}"

prefix="k8s-node-"
output="table"
while [ $# -gt 0 ]; do
  case "$1" in
    --prefix)
      prefix="$2"
      shift 2
      ;;
    -o | --output)
      output="$2"
      shift 2
      ;;
    *)
      echo "usage: $0 [--prefix NAME_PREFIX] [-o table|json]" >&2
      exit 2
      ;;
  esac
done

token="$(curl -fsS -X POST "$BASE_URL/oauth2/token" -H 'Content-Type: application/json' \
  -d "$(jq -n --arg id "$VERDA_CLIENT_ID" --arg secret "$VERDA_CLIENT_SECRET" \
    '{grant_type: "client_credentials", client_id: $id, client_secret: $secret}')" \
  | jq -r '.access_token')"

images="$(curl -fsS "$BASE_URL/volumes" -H "Authorization: Bearer $token" \
  | jq --arg prefix "$prefix" '
      [ .[] | select(.is_os_volume and .status == "detached" and (.name | startswith($prefix))) ]
      | sort_by(.created_at) | reverse')"

if [ "$output" = "json" ]; then
  echo "$images"
else
  {
    echo "NAME|ID|LOCATION|SIZE_GB|CREATED"
    echo "$images" | jq -r '.[] | [.name, .id, .location, .size, (.created_at | sub("\\.[0-9]+Z$"; "Z"))] | join("|")'
  } | column -t -s '|'
fi
