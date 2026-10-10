#!/bin/sh
set -eu

# Restore the backup produced by backup-stack.sh. The stack remains stopped
# after a restore so the operator can review the files before bringing it up.

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
env_file=${CYBERSTRIKE_ENV_FILE:-$repo_root/deploy/tools/.env}
backup_dir=${1:-}

die() {
  echo "restore failed: $*" >&2
  exit 1
}

[ -n "$backup_dir" ] || die "usage: $0 /path/to/backup-directory"
[ -d "$backup_dir" ] || die "backup directory not found: $backup_dir"
[ -f "$env_file" ] || die "missing $env_file"
command -v docker >/dev/null 2>&1 || die "docker is required"
docker compose version >/dev/null 2>&1 || die "Docker Compose v2 is required"

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

all_profiles="runtime-recon runtime-web runtime-identity runtime-binary runtime-cloud runtime-exploitation runtime-external-intel runtime-utility"
control_id=$(compose_control ps -q cyberstrike-ai 2>/dev/null || true)
if [ -z "$control_id" ]; then
  compose_control create cyberstrike-ai >/dev/null
  control_id=$(compose_control ps -q cyberstrike-ai 2>/dev/null || true)
fi
[ -n "$control_id" ] || die "could not create or find the control container"
helper_image=$(docker inspect --format '{{.Config.Image}}' "$control_id")

is_running=$(docker inspect --format '{{if .State.Running}}1{{else}}0{{end}}' "$control_id")
[ "$is_running" = "0" ] || die "stop the control plane before restoring"
for profile in $all_profiles; do
  service=$(profile_service "$profile")
  id=$(compose_runtime --profile "$profile" ps -q "$service" 2>/dev/null || true)
  if [ -n "$id" ]; then
    running=$(docker inspect --format '{{if .State.Running}}1{{else}}0{{end}}' "$id")
    [ "$running" = "0" ] || die "stop $service before restoring"
  fi
done

volume_for_destination() {
  destination=$1
  case "$destination" in
    /app/config) template='{{range .Mounts}}{{if eq .Destination "/app/config"}}{{.Name}}{{end}}{{end}}' ;;
    /app/data) template='{{range .Mounts}}{{if eq .Destination "/app/data"}}{{.Name}}{{end}}{{end}}' ;;
    /app/log) template='{{range .Mounts}}{{if eq .Destination "/app/log"}}{{.Name}}{{end}}{{end}}' ;;
    /app/chat_uploads) template='{{range .Mounts}}{{if eq .Destination "/app/chat_uploads"}}{{.Name}}{{end}}{{end}}' ;;
    /app/roles) template='{{range .Mounts}}{{if eq .Destination "/app/roles"}}{{.Name}}{{end}}{{end}}' ;;
    /app/skills) template='{{range .Mounts}}{{if eq .Destination "/app/skills"}}{{.Name}}{{end}}{{end}}' ;;
    /app/agents) template='{{range .Mounts}}{{if eq .Destination "/app/agents"}}{{.Name}}{{end}}{{end}}' ;;
    /app/knowledge_base) template='{{range .Mounts}}{{if eq .Destination "/app/knowledge_base"}}{{.Name}}{{end}}{{end}}' ;;
    *) return 1 ;;
  esac
  docker inspect "$control_id" --format "$template"
}

restore_volume() {
  destination=$1
  archive=$2
  volume=$(volume_for_destination "$destination")
  [ -n "$volume" ] || die "could not resolve named volume for $destination"
  [ -f "$backup_dir/$archive" ] || die "missing backup archive: $archive"
  docker run --rm \
    --entrypoint /bin/sh \
    --volume "$volume:/data" \
    --volume "$backup_dir:/backup:ro" \
    "$helper_image" \
    -ec "find /data -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +; tar xzf /backup/$archive -C /data"
}

restore_volume /app/config control-config.tgz
restore_volume /app/data control-data.tgz
restore_volume /app/log control-log.tgz
restore_volume /app/chat_uploads control-chat-uploads.tgz
restore_volume /app/roles control-roles.tgz
restore_volume /app/skills control-skills.tgz
restore_volume /app/agents control-agents.tgz
restore_volume /app/knowledge_base control-knowledge.tgz

if [ -f "$backup_dir/tool-data.tgz" ]; then
  tools_data="$repo_root/deploy/tools/data"
  if [ -d "$tools_data" ]; then
    mv "$tools_data" "${tools_data}.before-restore.$(date -u +%Y%m%dT%H%M%SZ)"
  fi
  mkdir -p "$repo_root/deploy/tools"
  tar xzf "$backup_dir/tool-data.tgz" -C "$repo_root/deploy/tools"
fi

echo "restore completed: $backup_dir"
echo "review the restored configuration, then run ./deploy/start-stack.sh"
