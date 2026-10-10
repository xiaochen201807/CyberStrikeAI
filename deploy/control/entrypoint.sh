#!/bin/sh
set -eu

CONFIG_PATH=${CYBERSTRIKE_CONFIG:-/app/config/config.yaml}
CONFIG_DIR=$(dirname "$CONFIG_PATH")
mkdir -p "$CONFIG_DIR"

if [ ! -f "$CONFIG_PATH" ]; then
  cp /app/config.example.yaml "$CONFIG_PATH"
  chmod 600 "$CONFIG_PATH"
  echo "Created $CONFIG_PATH from config.example.yaml; review AI credentials and security settings before use." >&2
fi

exec /app/bin/cyberstrike-ai --config "$CONFIG_PATH" "$@"
