#!/bin/sh
set -eu

. /scripts/target-allowlist.sh

target=${TARGET_URL:-}
reports_dir=${REPORTS_DIR:-/reports}

case "$target" in
  http://*|https://*) ;;
  *) echo "TARGET_URL must be an authorized http(s) URL" >&2; exit 2 ;;
esac
target_allowed "$target" || exit $?

mkdir -p "$reports_dir"
run_id=$(date -u +%Y%m%dT%H%M%SZ)
base="$reports_dir/zap-${run_id}"

echo "Starting ZAP baseline: $target"
zap-baseline.py \
  -t "$target" \
  -m "${ZAP_SPIDER_MINUTES:-1}" \
  -z "-config view.locale=${ZAP_LOCALE:-zh_CN}" \
  -r "${base}.html" \
  -J "${base}.json" \
  -I

chmod 0644 "${base}.html" "${base}.json" 2>/dev/null || true

echo "ZAP baseline completed; reports: ${base}.html and ${base}.json"
