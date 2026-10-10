#!/bin/sh
set -eu

. /scripts/target-allowlist.sh

targets_file=${TARGETS_FILE:-/targets/targets.txt}
reports_dir=${REPORTS_DIR:-/reports}
max_targets=${MAX_TARGETS:-100}

[ -f "$targets_file" ] || { echo "Target list not found: $targets_file" >&2; exit 1; }
mkdir -p "$reports_dir"

count=0
run_id=$(date -u +%Y%m%dT%H%M%SZ)

while IFS= read -r raw || [ -n "${raw:-}" ]; do
  target=$(printf '%s' "${raw:-}" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')
  case "$target" in
    ""|\#*) continue ;;
  esac
  target_allowed "$target" || exit $?
  count=$((count + 1))
  [ "$count" -le "$max_targets" ] || { echo "Target count exceeds MAX_TARGETS=$max_targets" >&2; exit 2; }

  prefix="$reports_dir/nuclei-${run_id}-${count}"
  echo "Starting Nuclei target $count: $target"
  nuclei \
    -u "$target" \
    -severity "${NUCLEI_SEVERITY:-medium,high,critical}" \
    -rate-limit "${NUCLEI_RATE_LIMIT:-30}" \
    -bulk-size "${NUCLEI_BULK_SIZE:-1}" \
    -concurrency "${NUCLEI_CONCURRENCY:-10}" \
    -o "${prefix}.txt" \
    -json-export "${prefix}.json" \
    -no-color \
    -no-interactsh < /dev/null
  chmod 0644 "${prefix}.txt" "${prefix}.json" 2>/dev/null || true
done < "$targets_file"

[ "$count" -gt 0 ] || { echo "No targets were found in $targets_file" >&2; exit 1; }
echo "Nuclei completed: $count target(s); reports in $reports_dir"
