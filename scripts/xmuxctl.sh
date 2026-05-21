#!/usr/bin/env bash
set -Eeuo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT_PATH="$ROOT_DIR/scripts/xmuxctl.sh"

APP_HOME="${XMUX_HOME:-$ROOT_DIR/.xmux}"
BIN_DIR="${XMUX_BIN_DIR:-$APP_HOME/bin}"
RUN_DIR="${XMUX_RUN_DIR:-$APP_HOME/run}"
LOG_DIR="${XMUX_LOG_DIR:-$APP_HOME/logs}"
APP_BIN="${XMUX_BIN:-$BIN_DIR/cloud-terminal}"
GO_BIN="${GO:-go}"
STOP_TIMEOUT="${XMUX_STOP_TIMEOUT:-15}"
RESTART_DELAY="${XMUX_RESTART_DELAY:-3}"

CONFIG_PATH=""
EXTRA_ARGS=()
MODES=(cloud agent local)

usage() {
  cat <<'EOF'
Usage:
  scripts/xmuxctl.sh start <cloud|agent|local> [-c config] [-- extra args]
  scripts/xmuxctl.sh stop <cloud|agent|local|all>
  scripts/xmuxctl.sh restart <cloud|agent|local> [-c config] [-- extra args]
  scripts/xmuxctl.sh upgrade <cloud|agent|local> [-c config] [-- extra args]
  scripts/xmuxctl.sh status [cloud|agent|local|all]
  scripts/xmuxctl.sh logs <cloud|agent|local> [-f]
  scripts/xmuxctl.sh build

Environment:
  XMUX_HOME           runtime home, default ./.xmux
  XMUX_BIN            managed binary path, default $XMUX_HOME/bin/cloud-terminal
  XMUX_STOP_TIMEOUT   seconds to wait before SIGKILL, default 15
  XMUX_RESTART_DELAY  seconds before supervised restart, default 3

Config resolution:
  -c/--config wins. Otherwise cloud uses ./cloud.yaml when present,
  agent uses ./agent.yaml when present, and all modes fall back to
  ./policy.yaml, then ./config/policy.yaml.
EOF
}

log() {
  printf '[%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$*"
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

ensure_dirs() {
  mkdir -p "$BIN_DIR" "$RUN_DIR" "$LOG_DIR"
}

valid_mode() {
  case "${1:-}" in
    cloud|agent|local) return 0 ;;
    *) die "invalid mode '$1', expected cloud, agent, or local" ;;
  esac
}

state_dir() {
  printf '%s/%s\n' "$RUN_DIR" "$1"
}

supervisor_pid_file() {
  printf '%s/supervisor.pid\n' "$(state_dir "$1")"
}

process_pid_file() {
  printf '%s/process.pid\n' "$(state_dir "$1")"
}

stop_file() {
  printf '%s/stop\n' "$(state_dir "$1")"
}

config_state_file() {
  printf '%s/config\n' "$(state_dir "$1")"
}

args_state_file() {
  printf '%s/args\n' "$(state_dir "$1")"
}

app_log_file() {
  printf '%s/%s.log\n' "$LOG_DIR" "$1"
}

supervisor_log_file() {
  printf '%s/%s-supervisor.log\n' "$LOG_DIR" "$1"
}

read_pid() {
  local file="$1"
  [[ -s "$file" ]] || return 1
  tr -d '[:space:]' <"$file"
}

pid_running() {
  local pid="${1:-}"
  [[ -n "$pid" ]] || return 1
  kill -0 "$pid" 2>/dev/null
}

pid_file_running() {
  local file="$1"
  local pid
  pid="$(read_pid "$file" 2>/dev/null || true)"
  pid_running "$pid"
}

service_running() {
  local mode="$1"
  pid_file_running "$(supervisor_pid_file "$mode")" || pid_file_running "$(process_pid_file "$mode")"
}

cleanup_stale_state() {
  local mode="$1"
  local supervisor_pid process_pid
  supervisor_pid="$(read_pid "$(supervisor_pid_file "$mode")" 2>/dev/null || true)"
  process_pid="$(read_pid "$(process_pid_file "$mode")" 2>/dev/null || true)"

  if [[ -n "$supervisor_pid" || -n "$process_pid" ]]; then
    if ! pid_running "$supervisor_pid" && ! pid_running "$process_pid"; then
      cleanup_state "$mode"
    fi
  fi
}

resolve_config() {
  local mode="$1"
  local path="$CONFIG_PATH"

  if [[ -z "$path" ]]; then
    case "$mode" in
      cloud)
        [[ -f "$ROOT_DIR/cloud.yaml" ]] && path="$ROOT_DIR/cloud.yaml"
        ;;
      agent)
        [[ -f "$ROOT_DIR/agent.yaml" ]] && path="$ROOT_DIR/agent.yaml"
        ;;
    esac
  fi

  if [[ -z "$path" && -f "$ROOT_DIR/policy.yaml" ]]; then
    path="$ROOT_DIR/policy.yaml"
  fi
  if [[ -z "$path" && -f "$ROOT_DIR/config/policy.yaml" ]]; then
    path="$ROOT_DIR/config/policy.yaml"
  fi
  if [[ -z "$path" ]]; then
    path="$ROOT_DIR/policy.yaml"
  fi

  case "$path" in
    /*) printf '%s\n' "$path" ;;
    *) printf '%s/%s\n' "$ROOT_DIR" "$path" ;;
  esac
}

parse_run_options() {
  CONFIG_PATH=""
  EXTRA_ARGS=()

  while [[ $# -gt 0 ]]; do
    case "$1" in
      -c|--config)
        local opt="$1"
        shift
        [[ $# -gt 0 ]] || die "$opt requires a value"
        CONFIG_PATH="$1"
        ;;
      --config=*)
        CONFIG_PATH="${1#*=}"
        ;;
      --)
        shift
        EXTRA_ARGS+=("$@")
        break
        ;;
      *)
        EXTRA_ARGS+=("$1")
        ;;
    esac
    shift || true
  done
}

build_binary() {
  ensure_dirs
  log "building $APP_BIN"
  (
    cd "$ROOT_DIR"
    "$GO_BIN" build -o "$APP_BIN" ./cmd/cloud-terminal
  )
}

ensure_binary() {
  if [[ ! -x "$APP_BIN" ]]; then
    build_binary
  fi
}

write_run_state() {
  local mode="$1"
  local config="$2"
  local sd
  sd="$(state_dir "$mode")"
  mkdir -p "$sd"
  printf '%s\n' "$config" >"$(config_state_file "$mode")"
  if [[ ${#EXTRA_ARGS[@]} -gt 0 ]]; then
    printf '%q ' "${EXTRA_ARGS[@]}" >"$(args_state_file "$mode")"
    printf '\n' >>"$(args_state_file "$mode")"
  else
    : >"$(args_state_file "$mode")"
  fi
}

wait_for_exit() {
  local pid="$1"
  local timeout="$2"
  local end=$((SECONDS + timeout))

  while pid_running "$pid"; do
    if (( SECONDS >= end )); then
      return 1
    fi
    sleep 1
  done
  return 0
}

cleanup_state() {
  local mode="$1"
  rm -f "$(supervisor_pid_file "$mode")" "$(process_pid_file "$mode")" "$(stop_file "$mode")"
}

start_service() {
  local mode="$1"
  valid_mode "$mode"
  ensure_dirs

  if service_running "$mode"; then
    local pid
    pid="$(read_pid "$(supervisor_pid_file "$mode")" 2>/dev/null || true)"
    [[ -n "$pid" ]] || pid="$(read_pid "$(process_pid_file "$mode")" 2>/dev/null || true)"
    log "$mode is already running (pid $pid)"
    return 0
  fi

  ensure_binary
  local config
  config="$(resolve_config "$mode")"
  write_run_state "$mode" "$config"
  rm -f "$(stop_file "$mode")"

  local supervisor_log
  supervisor_log="$(supervisor_log_file "$mode")"
  log "starting $mode with config $config"
  local supervise_cmd=("${BASH:-/bin/bash}" "$SCRIPT_PATH" __supervise "$mode" "$config" "$APP_BIN" --)
  if [[ ${#EXTRA_ARGS[@]} -gt 0 ]]; then
    supervise_cmd+=("${EXTRA_ARGS[@]}")
  fi
  nohup "${supervise_cmd[@]}" >>"$supervisor_log" 2>&1 &
  local supervisor_pid=$!
  printf '%s\n' "$supervisor_pid" >"$(supervisor_pid_file "$mode")"

  sleep 1
  if ! pid_running "$supervisor_pid"; then
    die "failed to start $mode supervisor, see $supervisor_log"
  fi

  log "$mode supervisor started (pid $supervisor_pid)"
  log "$mode log: $(app_log_file "$mode")"
}

stop_one() {
  local mode="$1"
  valid_mode "$mode"
  ensure_dirs
  cleanup_stale_state "$mode"

  local sd
  sd="$(state_dir "$mode")"
  mkdir -p "$sd"
  touch "$(stop_file "$mode")"

  local supervisor_pid process_pid
  supervisor_pid="$(read_pid "$(supervisor_pid_file "$mode")" 2>/dev/null || true)"
  process_pid="$(read_pid "$(process_pid_file "$mode")" 2>/dev/null || true)"

  if [[ -z "$supervisor_pid" && -z "$process_pid" ]]; then
    cleanup_state "$mode"
    log "$mode is not running"
    return 0
  fi

  if pid_running "$supervisor_pid"; then
    log "stopping $mode supervisor (pid $supervisor_pid)"
    kill -TERM "$supervisor_pid" 2>/dev/null || true
    if ! wait_for_exit "$supervisor_pid" "$STOP_TIMEOUT"; then
      log "$mode supervisor did not exit in ${STOP_TIMEOUT}s, sending SIGKILL"
      kill -KILL "$supervisor_pid" 2>/dev/null || true
    fi
  fi

  if pid_running "$process_pid"; then
    log "stopping $mode process (pid $process_pid)"
    kill -TERM "$process_pid" 2>/dev/null || true
    if ! wait_for_exit "$process_pid" "$STOP_TIMEOUT"; then
      log "$mode process did not exit in ${STOP_TIMEOUT}s, sending SIGKILL"
      kill -KILL "$process_pid" 2>/dev/null || true
    fi
  fi

  cleanup_state "$mode"
  log "$mode stopped"
}

restart_service() {
  local mode="$1"
  stop_one "$mode"
  start_service "$mode"
}

upgrade_service() {
  local mode="$1"
  valid_mode "$mode"
  log "upgrading $mode"
  build_binary
  stop_one "$mode"
  start_service "$mode"
}

status_one() {
  local mode="$1"
  valid_mode "$mode"
  cleanup_stale_state "$mode"
  local supervisor_pid process_pid config
  supervisor_pid="$(read_pid "$(supervisor_pid_file "$mode")" 2>/dev/null || true)"
  process_pid="$(read_pid "$(process_pid_file "$mode")" 2>/dev/null || true)"
  config="$(cat "$(config_state_file "$mode")" 2>/dev/null || true)"

  if service_running "$mode"; then
    printf '%-6s running  supervisor=%s process=%s config=%s log=%s\n' \
      "$mode" "${supervisor_pid:-"-"}" "${process_pid:-"-"}" "${config:-"-"}" "$(app_log_file "$mode")"
  else
    printf '%-6s stopped  log=%s\n' "$mode" "$(app_log_file "$mode")"
  fi
}

logs_service() {
  local mode="$1"
  valid_mode "$mode"
  local follow="${2:-}"
  local file
  file="$(app_log_file "$mode")"
  if [[ "$follow" == "-f" || "$follow" == "--follow" ]]; then
    tail -f "$file"
  else
    tail -n 120 "$file"
  fi
}

SUPERVISED_PID=""
CURRENT_MODE=""

supervisor_shutdown() {
  local mode="$CURRENT_MODE"
  [[ -n "$mode" ]] && touch "$(stop_file "$mode")"
  if pid_running "$SUPERVISED_PID"; then
    kill -TERM "$SUPERVISED_PID" 2>/dev/null || true
    wait "$SUPERVISED_PID" 2>/dev/null || true
  fi
  [[ -n "$mode" ]] && cleanup_state "$mode"
  exit 0
}

supervise() {
  local mode="$1"
  local config="$2"
  local bin="$3"
  shift 3
  if [[ "${1:-}" == "--" ]]; then
    shift
  fi

  valid_mode "$mode"
  ensure_dirs
  CURRENT_MODE="$mode"
  mkdir -p "$(state_dir "$mode")"
  printf '%s\n' "$$" >"$(supervisor_pid_file "$mode")"
  trap supervisor_shutdown TERM INT QUIT

  while true; do
    if [[ -f "$(stop_file "$mode")" ]]; then
      break
    fi

    log "supervisor starting $mode"
    "$bin" -mode "$mode" -config "$config" "$@" >>"$(app_log_file "$mode")" 2>&1 &
    SUPERVISED_PID=$!
    printf '%s\n' "$SUPERVISED_PID" >"$(process_pid_file "$mode")"

    set +e
    wait "$SUPERVISED_PID"
    local exit_code=$?
    set -e
    SUPERVISED_PID=""
    rm -f "$(process_pid_file "$mode")"

    if [[ -f "$(stop_file "$mode")" ]]; then
      break
    fi

    log "$mode exited with code $exit_code; restarting in ${RESTART_DELAY}s"
    sleep "$RESTART_DELAY"
  done

  cleanup_state "$mode"
  log "$mode supervisor stopped"
}

main() {
  local command="${1:-}"
  [[ -n "$command" ]] || {
    usage
    exit 1
  }
  shift || true

  case "$command" in
    start)
      [[ $# -ge 1 ]] || die "start requires a mode"
      local mode="$1"
      shift
      parse_run_options "$@"
      start_service "$mode"
      ;;
    stop)
      [[ $# -ge 1 ]] || die "stop requires a mode or all"
      if [[ "$1" == "all" ]]; then
        for mode in "${MODES[@]}"; do
          stop_one "$mode"
        done
      else
        stop_one "$1"
      fi
      ;;
    restart)
      [[ $# -ge 1 ]] || die "restart requires a mode"
      local mode="$1"
      shift
      parse_run_options "$@"
      restart_service "$mode"
      ;;
    upgrade)
      [[ $# -ge 1 ]] || die "upgrade requires a mode"
      local mode="$1"
      shift
      parse_run_options "$@"
      upgrade_service "$mode"
      ;;
    status)
      local target="${1:-all}"
      if [[ "$target" == "all" ]]; then
        for mode in "${MODES[@]}"; do
          status_one "$mode"
        done
      else
        status_one "$target"
      fi
      ;;
    logs)
      [[ $# -ge 1 ]] || die "logs requires a mode"
      local mode="$1"
      shift
      logs_service "$mode" "${1:-}"
      ;;
    build)
      build_binary
      ;;
    __supervise)
      supervise "$@"
      ;;
    -h|--help|help)
      usage
      ;;
    *)
      usage
      die "unknown command '$command'"
      ;;
  esac
}

main "$@"
