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

if [ ! -f "$env_file" ]; then
  echo "missing $env_file" >&2
  echo "copy deploy/tools/.env.example to deploy/tools/.env, set TOOL_MCP_TOKEN and TOOL_ALLOWED_HOSTS, then retry" >&2
  exit 2
fi

compose_up() {
  compose_file=$1
  shift
  if [ "$build_images" = "1" ]; then
    docker compose --env-file "$env_file" -f "$compose_file" "$@" up -d --build
  else
    docker compose --env-file "$env_file" -f "$compose_file" "$@" up -d
  fi
}

compose_up "$repo_root/deploy/control/docker-compose.yml"

set --
for profile in $runtime_profiles; do
  set -- "$@" --profile "$profile"
done

if [ "$#" -eq 0 ]; then
  echo "RUNTIME_PROFILES must contain at least one Compose profile" >&2
  exit 2
fi

compose_up "$repo_root/deploy/tools/docker-compose.runtime.yml" "$@"
echo "CyberStrikeAI control plane and tool families are starting. Check with: docker compose -f deploy/control/docker-compose.yml ps"
