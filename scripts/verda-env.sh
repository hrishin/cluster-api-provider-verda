#!/usr/bin/env bash
# Print the Verda API credentials as exports: from VERDA_CLIENT_ID /
# VERDA_CLIENT_SECRET when set, else from the verda CLI's credentials file
# (~/.verda/credentials, INI, profile "default" or $VERDA_PROFILE):
#   eval "$(scripts/verda-env.sh)"
# The same file the provider's manager reads (cloud.LoadCredentials).
set -euo pipefail
file="${VERDA_CREDENTIALS_FILE:-$HOME/.verda/credentials}"
profile="${VERDA_PROFILE:-default}"

id="${VERDA_CLIENT_ID:-}"
secret="${VERDA_CLIENT_SECRET:-}"
base_url="${VERDA_BASE_URL:-}"
if { [ -z "$id" ] || [ -z "$secret" ]; } && [ -f "$file" ]; then
  in_profile=0
  while IFS= read -r line || [ -n "$line" ]; do
    line="${line#"${line%%[![:space:]]*}"}"
    case "$line" in
      "" | \#* | \;*) continue ;;
      \[*\])
        [ "$line" = "[$profile]" ] && in_profile=1 || in_profile=0
        continue
        ;;
    esac
    [ "$in_profile" = 1 ] || continue
    key="${line%%=*}"
    key="${key%"${key##*[![:space:]]}"}"
    value="${line#*=}"
    value="${value#"${value%%[![:space:]]*}"}"
    case "$key" in
      verda_client_id) [ -n "$id" ] || id="$value" ;;
      verda_client_secret) [ -n "$secret" ] || secret="$value" ;;
      verda_base_url) [ -n "$base_url" ] || base_url="$value" ;;
    esac
  done <"$file"
fi
[ -n "$id" ] && [ -n "$secret" ] || {
  echo "no Verda credentials: set VERDA_CLIENT_ID and VERDA_CLIENT_SECRET or add profile [$profile] to $file" >&2
  exit 1
}
printf 'export VERDA_CLIENT_ID=%q\n' "$id"
printf 'export VERDA_CLIENT_SECRET=%q\n' "$secret"
[ -z "$base_url" ] || printf 'export VERDA_BASE_URL=%q\n' "$base_url"
