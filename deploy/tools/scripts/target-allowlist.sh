#!/bin/sh

# Shared target parser for one-shot tool jobs. The MCP adapter performs the
# same policy check in Go; keeping it here prevents a Compose job from
# bypassing the allowlist simply by mounting a different targets file.

target_host() {
  target=$1
  case "$target" in
    http://*|https://*) ;;
    *) echo "Only http:// and https:// targets are accepted: $target" >&2; return 2 ;;
  esac
  case "$target" in
    *[[:space:]]*) echo "Target contains whitespace: $target" >&2; return 2 ;;
  esac

  rest=${target#*://}
  hostport=${rest%%/*}
  hostport=${hostport%%\?*}
  hostport=${hostport%%\#*}
  [ -n "$hostport" ] || { echo "Target host is empty: $target" >&2; return 2; }
  case "$hostport" in
    *'@'*|*'['*|*']'*|*'\'*) echo "Target contains unsupported userinfo or IPv6: $target" >&2; return 2 ;;
  esac

  host=$hostport
  case "$hostport" in
    *:*)
      case "$hostport" in
        *:*:*) echo "IPv6 targets are not supported by this job wrapper: $target" >&2; return 2 ;;
      esac
      port=${hostport##*:}
      case "$port" in
        ''|*[!0-9]*) echo "Target port is invalid: $target" >&2; return 2 ;;
      esac
      host=${hostport%:*}
      ;;
  esac
  case "$host" in
    ''|*[!A-Za-z0-9.-]*) echo "Target host is invalid: $target" >&2; return 2 ;;
  esac
  host=$(printf '%s' "$host" | tr '[:upper:]' '[:lower:]' | sed 's/[.]$//')
  [ -n "$host" ] || { echo "Target host is empty: $target" >&2; return 2; }
  printf '%s\n' "$host"
}

target_allowed() {
  target=$1
  host=$(target_host "$target") || return $?
  allowed_hosts=${TOOL_ALLOWED_HOSTS:-}
  [ -n "$allowed_hosts" ] || {
    echo "TOOL_ALLOWED_HOSTS must contain an exact host or *.example.com pattern" >&2
    return 2
  }

  matched=1
  invalid=0
  while IFS= read -r pattern; do
    pattern=$(printf '%s' "$pattern" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | tr '[:upper:]' '[:lower:]' | sed 's/[.]$//')
    [ -n "$pattern" ] || continue
    case "$pattern" in
      \*.*)
        suffix=${pattern#\*}
        case "$suffix" in
          .[A-Za-z0-9.-]*) ;;
          *) invalid=1; continue ;;
        esac
        case "$host" in
          *"$suffix")
            base=${suffix#.}
            [ "$host" != "$base" ] && matched=0
            ;;
        esac
        ;;
      *'*'*|*'?'*) invalid=1 ;;
      *[!A-Za-z0-9.-]*) invalid=1 ;;
      "$host") matched=0 ;;
    esac
  done <<EOF
$(printf '%s' "$allowed_hosts" | tr ',' '\n')
EOF

  [ "$invalid" -eq 0 ] || {
    echo "TOOL_ALLOWED_HOSTS contains an invalid host pattern" >&2
    return 2
  }
  [ "$matched" -eq 0 ] || {
    echo "Target host $host is outside TOOL_ALLOWED_HOSTS" >&2
    return 1
  }
}
