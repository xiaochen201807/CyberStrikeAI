#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
CATALOG="$ROOT/catalog.yaml"
INVENTORY="$ROOT/inventory.yaml"
ENV_FILE="$ROOT/.env.example"
if [ -f "$ROOT/.env" ]; then
  ENV_FILE="$ROOT/.env"
fi

cd "$ROOT/../.."
go run ./cmd/toolcatalog validate --compose "$ROOT/docker-compose.yml" "$CATALOG"

if [ -f "$INVENTORY" ]; then
  tmp_inventory=$(mktemp)
  trap 'rm -f "$tmp_inventory"' EXIT HUP INT TERM
  go run ./cmd/toolcatalog inventory --tools-dir tools --catalog "$CATALOG" --output "$tmp_inventory" >/dev/null
  if ! diff -u "$INVENTORY" "$tmp_inventory"; then
    echo "deploy/tools/inventory.yaml is stale; regenerate it with:" >&2
    echo "  go run ./cmd/toolcatalog inventory --tools-dir tools --catalog deploy/tools/catalog.yaml --output deploy/tools/inventory.yaml" >&2
    exit 1
  fi
fi

if [ "${REQUIRE_IMAGE_DIGESTS:-0}" = "1" ]; then
  go run ./cmd/toolcatalog validate --require-digests --compose "$ROOT/docker-compose.yml" "$CATALOG"
fi

if [ "${REQUIRE_SECURE_TOKEN:-0}" = "1" ]; then
  token=$(awk -F= '$1 == "TOOL_MCP_TOKEN" { sub(/^[^=]*=/, ""); print; exit }' "$ENV_FILE")
  case "$token" in
    ""|replace-with*) echo "TOOL_MCP_TOKEN is empty or still a placeholder" >&2; exit 1 ;;
  esac
  if [ "${#token}" -lt 32 ]; then
    echo "TOOL_MCP_TOKEN must be at least 32 characters" >&2
    exit 1
  fi
fi

# The generic compatibility adapter is opt-in. If it is configured, require
# an immutable runtime image before allowing a production validation to pass.
runtime_image=$(awk -F= '$1 == "TOOL_RUNTIME_IMAGE" { sub(/^[^=]*=/, ""); print; exit }' "$ENV_FILE" 2>/dev/null || true)
if [ -n "$runtime_image" ] && [ "${REQUIRE_IMAGE_DIGESTS:-0}" = "1" ]; then
  if ! printf '%s\n' "$runtime_image" | grep -Eq '@sha256:[0-9a-fA-F]{64}$'; then
    echo "TOOL_RUNTIME_IMAGE must include a sha256 digest when configured" >&2
    exit 1
  fi
fi

if command -v docker >/dev/null 2>&1; then
  docker compose -f "$ROOT/docker-compose.yml" --env-file "$ENV_FILE" config --quiet
  if [ "${REQUIRE_IMAGE_DIGESTS:-0}" = "1" ]; then
    resolved=$(docker compose \
      -f "$ROOT/docker-compose.yml" \
      --env-file "$ENV_FILE" \
      --profile web-mcp \
      --profile web-job \
      --profile baseline-mcp \
      --profile baseline-job \
      config)
    unpinned=$(printf '%s\n' "$resolved" | awk '/^[[:space:]]*(image|TOOL_BASE_IMAGE):/ {print $2}' | grep -Ev '@sha256:[0-9a-fA-F]{64}$' || true)
    if [ -n "$unpinned" ]; then
      echo "resolved Compose tool images must use sha256 digests:" >&2
      printf '%s\n' "$unpinned" >&2
      exit 1
    fi
  fi
fi

echo "tool plane configuration is valid"
