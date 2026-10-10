#!/bin/sh
set -eu

# Verify a running single-VM deployment without requiring Go or the source
# tree's build toolchain. The script is Docker/Compose-only so it can be
# copied with a release package and used after an image upgrade.

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
env_file=${CYBERSTRIKE_ENV_FILE:-$repo_root/deploy/tools/.env}
runtime_profiles=${RUNTIME_PROFILES:-"runtime-recon runtime-web"}
timeout_seconds=${STACK_CHECK_TIMEOUT_SECONDS:-180}
interval_seconds=${STACK_CHECK_INTERVAL_SECONDS:-3}

die() {
  echo "stack check failed: $*" >&2
  exit 1
}

[ -f "$env_file" ] || die "missing $env_file; copy deploy/tools/.env.example first"
command -v docker >/dev/null 2>&1 || die "docker is required"
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is required"

env_value() {
  key=$1
  awk -F= -v key="$key" '$1 == key { sub(/^[^=]*=/, ""); print; exit }' "$env_file"
}

token=$(env_value TOOL_MCP_TOKEN)
allowed_hosts=$(env_value TOOL_ALLOWED_HOSTS)
[ "${#token}" -ge 32 ] || die "TOOL_MCP_TOKEN must be at least 32 characters"
case "$token" in
  replace-with*|*" "*) die "TOOL_MCP_TOKEN is still a placeholder or contains whitespace" ;;
esac
[ -n "$allowed_hosts" ] || die "TOOL_ALLOWED_HOSTS must not be empty"
case ",$allowed_hosts," in
  *,\*,*) die "TOOL_ALLOWED_HOSTS must not contain the global wildcard '*'" ;;
esac

compose_control() {
  docker compose --env-file "$env_file" -f "$repo_root/deploy/control/docker-compose.yml" "$@"
}

compose_runtime() {
  docker compose --env-file "$env_file" -f "$repo_root/deploy/tools/docker-compose.runtime.yml" "$@"
}

profile_service() {
  case "$1" in
    runtime-recon) printf '%s\n' runtime-recon-mcp ;;
    runtime-web) printf '%s\n' runtime-web-mcp ;;
    runtime-identity) printf '%s\n' runtime-identity-mcp ;;
    runtime-binary) printf '%s\n' runtime-binary-mcp ;;
    runtime-cloud) printf '%s\n' runtime-cloud-mcp ;;
    runtime-exploitation) printf '%s\n' runtime-exploitation-mcp ;;
    runtime-external-intel) printf '%s\n' runtime-external-intel-mcp ;;
    runtime-utility) printf '%s\n' runtime-utility-mcp ;;
    *) return 1 ;;
  esac
}

for profile in $runtime_profiles; do
  profile_service "$profile" >/dev/null || die "unsupported RUNTIME_PROFILES entry: $profile"
done

dump_diagnostics() {
  echo "--- control status ---" >&2
  compose_control ps >&2 || true
  echo "--- runtime status ---" >&2
  for profile in $runtime_profiles; do
    service=$(profile_service "$profile")
    compose_runtime --profile "$profile" ps "$service" >&2 || true
  done
  echo "--- control logs ---" >&2
  compose_control logs --tail=80 cyberstrike-ai >&2 || true
  for profile in $runtime_profiles; do
    service=$(profile_service "$profile")
    echo "--- $service logs ---" >&2
    compose_runtime --profile "$profile" logs --tail=80 "$service" >&2 || true
  done
}

control_endpoint() {
  spec=$(compose_control port cyberstrike-ai 8080 2>/dev/null || true)
  [ -n "$spec" ] || return 1
  case "$spec" in
    \[*\]:*) host=${spec#\[}; host=${host%%\]*}; port=${spec##*:} ;;
    *:*) host=${spec%:*}; port=${spec##*:} ;;
    *) return 1 ;;
  esac
  case "$host" in
    0.0.0.0|::|"") host=127.0.0.1 ;;
  esac
  printf '%s\n' "$host:$port"
}

probe_control() {
  endpoint=$(control_endpoint) || return 1
  host=${endpoint%:*}
  port=${endpoint##*:}
  curl -kfsS --max-time 5 "https://${host}:${port}/readyz" >/dev/null 2>&1 \
    || curl -fsS --max-time 5 "http://${host}:${port}/readyz" >/dev/null 2>&1
}

wait_for_control() {
  deadline=$(( $(date +%s) + timeout_seconds ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if probe_control; then
      echo "control plane ready ($(control_endpoint))"
      return 0
    fi
    sleep "$interval_seconds"
  done
  return 1
}

probe_runtime() {
  profile=$1
  service=$(profile_service "$profile")
  # -probe validates the mounted catalog, selected family and the HTTP health
  # endpoint from inside the container. It does not execute a scanner.
  compose_runtime --profile "$profile" exec -T "$service" /usr/local/bin/tool-mcp -probe >/dev/null 2>&1
}

wait_for_runtime() {
  profile=$1
  service=$(profile_service "$profile")
  deadline=$(( $(date +%s) + timeout_seconds ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if probe_runtime "$profile"; then
      echo "$service ready"
      return 0
    fi
    sleep "$interval_seconds"
  done
  return 1
}

wait_for_control || {
  dump_diagnostics
  die "control plane did not become ready within ${timeout_seconds}s"
}

for profile in $runtime_profiles; do
  if ! wait_for_runtime "$profile"; then
    dump_diagnostics
    die "$(profile_service "$profile") did not become ready within ${timeout_seconds}s"
  fi
done

echo "stack check passed"
