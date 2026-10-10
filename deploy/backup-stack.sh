#!/bin/sh
set -eu

# Consistent single-VM backup for the control-plane named volumes and the
# tool-plane bind-mounted data. The stack is stopped briefly so SQLite,
# reports and uploads are captured at one point in time.

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/.." && pwd)
env_file=${CYBERSTRIKE_ENV_FILE:-$repo_root/deploy/tools/.env}
backup_root=${CYBERSTRIKE_BACKUP_DIR:-$repo_root/backups}
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
backup_dir="$backup_root/$timestamp"

die() {
  echo "backup failed: $*" >&2
  exit 1
}

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
[ -n "$control_id" ] || die "control container does not exist; start the stack first"
helper_image=$(docker inspect --format '{{.Config.Image}}' "$control_id")

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

mkdir -p "$backup_dir"
chmod 700 "$backup_dir"

control_running=$(docker inspect --format '{{if .State.Running}}1{{else}}0{{end}}' "$control_id")
runtime_running=""
for profile in $all_profiles; do
  service=$(profile_service "$profile")
  id=$(compose_runtime --profile "$profile" ps -q "$service" 2>/dev/null || true)
  if [ -n "$id" ]; then
    running=$(docker inspect --format '{{if .State.Running}}1{{else}}0{{end}}' "$id")
  else
    running=0
  fi
  runtime_running="$runtime_running\n$profile=$running"
done

restore_running() {
  if [ "$control_running" = "1" ]; then
    compose_control start cyberstrike-ai >/dev/null 2>&1 || true
  fi
  printf '%b\n' "$runtime_running" | while IFS='=' read -r profile running; do
    [ "$running" = "1" ] || continue
    service=$(profile_service "$profile") || continue
    compose_runtime --profile "$profile" start "$service" >/dev/null 2>&1 || true
  done
}
trap restore_running EXIT HUP INT TERM

compose_control stop cyberstrike-ai >/dev/null
for profile in $all_profiles; do
  service=$(profile_service "$profile")
  compose_runtime --profile "$profile" stop "$service" >/dev/null 2>&1 || true
done

for entry in \
  'config:/app/config:control-config.tgz' \
  'data:/app/data:control-data.tgz' \
  'log:/app/log:control-log.tgz' \
  'chat_uploads:/app/chat_uploads:control-chat-uploads.tgz' \
  'roles:/app/roles:control-roles.tgz' \
  'skills:/app/skills:control-skills.tgz' \
  'agents:/app/agents:control-agents.tgz' \
  'knowledge_base:/app/knowledge_base:control-knowledge.tgz'; do
  name=${entry%%:*}
  rest=${entry#*:}
  destination=${rest%%:*}
  archive=${rest##*:}
  volume=$(volume_for_destination "$destination")
  [ -n "$volume" ] || die "could not resolve named volume for $destination"
  docker run --rm \
    --entrypoint /bin/sh \
    --volume "$volume:/data:ro" \
    --volume "$backup_dir:/backup" \
    "$helper_image" \
    -ec "tar czf /backup/$archive -C /data ."
  printf '%s=%s\n' "$name" "$volume" >> "$backup_dir/volumes.env"
done

if [ -d "$repo_root/deploy/tools/data" ]; then
  tar czf "$backup_dir/tool-data.tgz" -C "$repo_root/deploy/tools" data
fi

if [ -f "$env_file" ]; then
  cp "$env_file" "$backup_dir/tools.env.backup"
  chmod 600 "$backup_dir/tools.env.backup"
fi
if [ -f "$repo_root/deploy/control/.env" ]; then
  cp "$repo_root/deploy/control/.env" "$backup_dir/control.env.backup"
  chmod 600 "$backup_dir/control.env.backup"
fi

{
  printf 'created_at=%s\n' "$timestamp"
  printf 'git_commit=%s\n' "$(git -C "$repo_root" rev-parse HEAD 2>/dev/null || printf unknown)"
  printf 'control_running=%s\n' "$control_running"
  printf 'runtime_profiles=%s\n' "$all_profiles"
} > "$backup_dir/manifest.env"
chmod 600 "$backup_dir/manifest.env" "$backup_dir/volumes.env"

trap - EXIT HUP INT TERM
restore_running
echo "backup created: $backup_dir"
