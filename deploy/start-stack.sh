#!/bin/sh
set -eu

# Bring up the control plane first so it creates the shared Docker network,
# then start only the tool families selected by RUNTIME_PROFILES.  This keeps
# local/VM deployment to one command without merging the two Compose projects
# (the runtime project intentionally treats the control network as external).

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
env_file=${CYBERSTRIKE_ENV_FILE:-$repo_root/deploy/tools/.env}
build_images=${BUILD_IMAGES:-1}
runtime_profiles=${RUNTIME_PROFILES:-"runtime-recon runtime-web"}

case "$build_images" in
  0|1) ;;
  *) echo "BUILD_IMAGES must be 0 or 1" >&2; exit 2 ;;
esac

if [ ! -f "$env_file" ]; then
  echo "missing $env_file" >&2
  echo "copy deploy/tools/.env.example to deploy/tools/.env, set TOOL_MCP_TOKEN and TOOL_ALLOWED_HOSTS, then retry" >&2
  exit 2
fi

command -v docker >/dev/null 2>&1 || { echo "docker is required" >&2; exit 2; }
docker compose version >/dev/null 2>&1 || { echo "Docker Compose v2 is required" >&2; exit 2; }

env_value() {
  key=$1
  awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; exit }' "$env_file"
}

token=$(env_value TOOL_MCP_TOKEN)
allowed_hosts=$(env_value TOOL_ALLOWED_HOSTS)
[ "${#token}" -ge 32 ] || { echo "TOOL_MCP_TOKEN must be at least 32 characters" >&2; exit 2; }
case "$token" in
  replace-with*|*" "*) echo "TOOL_MCP_TOKEN is still a placeholder or contains whitespace" >&2; exit 2 ;;
esac
[ -n "$allowed_hosts" ] || { echo "TOOL_ALLOWED_HOSTS must not be empty" >&2; exit 2; }
case ",$allowed_hosts," in
  *,\*,*) echo "TOOL_ALLOWED_HOSTS must not contain the global wildcard '*'" >&2; exit 2 ;;
esac

image_key_for_profile() {
  case "$1" in
    runtime-recon) printf '%s\n' RECON_IMAGE ;;
    runtime-web) printf '%s\n' WEB_IMAGE ;;
    runtime-identity) printf '%s\n' IDENTITY_IMAGE ;;
    runtime-binary) printf '%s\n' BINARY_IMAGE ;;
    runtime-cloud) printf '%s\n' CLOUD_IMAGE ;;
    runtime-exploitation) printf '%s\n' EXPLOITATION_IMAGE ;;
    runtime-external-intel) printf '%s\n' EXTERNAL_INTEL_IMAGE ;;
    runtime-utility) printf '%s\n' UTILITY_IMAGE ;;
    *) return 1 ;;
  esac
}

compose_up() {
  compose_file=$1
  shift
  if [ "$build_images" = "1" ]; then
    docker compose --env-file "$env_file" -f "$compose_file" "$@" up -d --build
  else
    docker compose --env-file "$env_file" -f "$compose_file" "$@" up -d
  fi
}

compose_pull() {
  compose_file=$1
  shift
  docker compose --env-file "$env_file" -f "$compose_file" "$@" pull
}

set --
for profile in $runtime_profiles; do
  image_key_for_profile "$profile" >/dev/null || {
    echo "unsupported RUNTIME_PROFILES entry: $profile" >&2
    exit 2
  }
  set -- "$@" --profile "$profile"
done

if [ "$#" -eq 0 ]; then
  echo "RUNTIME_PROFILES must contain at least one Compose profile" >&2
  exit 2
fi

if [ "$build_images" = "0" ]; then
  # Production mode pulls the immutable references from .env instead of
  # silently building a local image from a different checkout.
  control_image=$(env_value CYBERSTRIKE_IMAGE)
  printf '%s\n' "$control_image" | grep -Eq '@sha256:[0-9a-fA-F]{64}$' || {
    echo "CYBERSTRIKE_IMAGE must be a published image with a sha256 digest when BUILD_IMAGES=0" >&2
    exit 2
  }
  for profile in $runtime_profiles; do
    key=$(image_key_for_profile "$profile")
    image=$(env_value "$key")
    printf '%s\n' "$image" | grep -Eq '@sha256:[0-9a-fA-F]{64}$' || {
      echo "$key must be a published image with a sha256 digest when BUILD_IMAGES=0" >&2
      exit 2
    }
  done
  compose_pull "$repo_root/deploy/control/docker-compose.yml"
fi

compose_up "$repo_root/deploy/control/docker-compose.yml"

if [ "$build_images" = "0" ]; then
  compose_pull "$repo_root/deploy/tools/docker-compose.runtime.yml" "$@"
fi

compose_up "$repo_root/deploy/tools/docker-compose.runtime.yml" "$@"

RUNTIME_PROFILES="$runtime_profiles" \
  CYBERSTRIKE_ENV_FILE="$env_file" \
  "$repo_root/deploy/check-stack.sh"
