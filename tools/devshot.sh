#!/bin/bash
# Run the scripted devshot scenarios (internal/devdemos.go): Aviary's test suite
# for anything that only exists inside the engine — editors, placement, terrain,
# undo — driven through the same calls the editors make.
#
#   tools/devshot.sh                  # every scenario, headless
#   tools/devshot.sh scenery terrain  # just these
#   tools/devshot.sh --shot           # windowed, one at a time, saving PNGs
#   DEVSHOT_JOBS=1 tools/devshot.sh   # headless, one at a time
#
# Headless runs open no window and run several at a time (a quarter of the
# cores by default). They cannot take screenshots — the dummy renderer has no
# frame to grab — so the log is the result: a scenario fails if it logs
# "devshot <name>: ERROR", panics, raises an engine ERROR after the world has
# loaded, or never reaches the end of its script.
#
# --shot runs the scenarios in a real window instead and saves each snap to
# $DEVSHOT_OUT (default /tmp/aviary-devshot) as <scenario>-<label>.png, plus a
# final <scenario>.png. A window steals focus, so these run one at a time.
#
# Every scenario gets its own scratch profile (XDG_DATA_HOME/XDG_CONFIG_HOME in
# a temp dir): a fresh, empty offline world, no saves or user.json touched. The
# library packs are symlinked in from your real user dir ($AVIARY_USER_DIR,
# default ~/.local/share/aviary) with downloads off, so nothing writes to them.
#
# The engine runs with a small, explicit environment rather than yours: the
# static musl build used to read at most 4KiB of /proc/self/environ, and a long
# shell environment silently dropped the AVIARY_* variables (fixed in graphics.gd,
# but a clean environment keeps the runs independent of the shell either way).
set -uo pipefail
cd "$(dirname "$0")/.."

SCENARIOS=(
  boot
  editors
  scenery
  terrain
  critter
)

# Engine errors that are the environment, not Aviary: the dynamic sentry addon
# can't load in the static build, and there is no OpenXR runtime.
NOISE='sentry|OpenXR|xrEnumerate'

# run_one runs a single scenario and prints its one-line result. The script
# re-invokes ITSELF with --one to run these concurrently, so everything above
# the scenario loop (the rebuild) happens once.
run_one() {
  local name=$1
  local profile out code bad ran window errs status
  profile=$(mktemp -d -t aviary-devshot-XXXXXX)
  mkdir -p "$profile/data/aviary" "$profile/config"
  ln -s "$DEVSHOT_USER_DIR/library.pck" "$DEVSHOT_USER_DIR/preview.pck" "$profile/data/aviary/"
  local display=(--headless)
  local shot=()
  if [ -n "${DEVSHOT_SHOT:-}" ]; then
    display=()
    shot=(AVIARY_SHOT="$DEVSHOT_OUT/$name.png")
  fi
  out=$(cd graphics && timeout 300 env -i \
    HOME="$HOME" PATH="$PATH" LANG="${LANG:-C.UTF-8}" \
    ${XDG_RUNTIME_DIR:+XDG_RUNTIME_DIR=$XDG_RUNTIME_DIR} \
    ${WAYLAND_DISPLAY:+WAYLAND_DISPLAY=$WAYLAND_DISPLAY} \
    ${DISPLAY:+DISPLAY=$DISPLAY} \
    XDG_DATA_HOME="$profile/data" XDG_CONFIG_HOME="$profile/config" \
    AVIARY_DOWNLOAD=0 AVIARY_DEMO="$name" "${shot[@]}" \
    ${AVIARY_DEMO_TIMEOUT:+AVIARY_DEMO_TIMEOUT=$AVIARY_DEMO_TIMEOUT} \
    "$DEVSHOT_ENGINE" "${display[@]}" 2>&1)
  code=$?
  rm -rf "$profile"
  [ -n "${DEVSHOT_LOGS:-}" ] && echo "$out" > "$DEVSHOT_LOGS/$name.log"
  bad=$(echo "$out" | grep -E 'devshot.*ERROR|panic:')
  # Engine errors while the scenario ran: from its first step (the world is
  # loaded by then, so no startup noise) to its last (not the exit-time leak
  # report, which is teardown's business, not the scenario's).
  window=$(echo "$out" | sed -n "/^devshot $name: begin/,/^devshot state: $name/p")
  errs=$(echo "$window" | grep -E '^(ERROR|SCRIPT ERROR):' | grep -vE "$NOISE")
  # The harness prints "devshot state:" as it quits; the exit status can't say
  # the scenario finished (the engine may still fail on the way out).
  ran=$(echo "$out" | grep -c "^devshot state: $name")
  status=ok
  { [ "$ran" -eq 0 ] || [ -n "$bad" ] || [ -n "$errs" ]; } && status=FAILED
  printf '%-10s %-7s exit=%s\n' "$name" "$status" "$code"
  echo "$out" | grep -E "^devshot $name: " | grep -vE 'ERROR|: begin|: state:' | sed 's/^devshot [^:]*: /    /'
  [ -n "$bad" ] && echo "$bad" | head -8 | sed 's/^/    /'
  [ -n "$errs" ] && echo "$window" | grep -vE "$NOISE" | grep -E -A1 '^(ERROR|SCRIPT ERROR):' | head -8 | sed 's/^/    /'
  [ "$ran" -eq 0 ] && echo "$out" | grep -vE "$NOISE" | tail -6 | sed 's/^/    /'
  [ "$status" = ok ]
}

if [ "${1:-}" = "--one" ]; then
  shift
  run_one "$@"
  exit $?
fi

if [ "${1:-}" = "--shot" ]; then
  shift
  export DEVSHOT_SHOT=1
  export DEVSHOT_OUT=${DEVSHOT_OUT:-/tmp/aviary-devshot}
  mkdir -p "$DEVSHOT_OUT"
fi

for name in "$@"; do
  printf '%s\n' "${SCENARIOS[@]}" | grep -qx "$name" || { echo "no such scenario: $name (have: ${SCENARIOS[*]})"; exit 2; }
done

export DEVSHOT_USER_DIR=${AVIARY_USER_DIR:-$HOME/.local/share/aviary}
if [ ! -f "$DEVSHOT_USER_DIR/preview.pck" ] || [ ! -f "$DEVSHOT_USER_DIR/library.pck" ]; then
  echo "no library packs in $DEVSHOT_USER_DIR (run Aviary once to download them, or set AVIARY_USER_DIR)"
  exit 1
fi

# The engine binary has the game linked into it, so a source change means a
# rebuild before any of this means anything. AVIARY_BUILD_ONLY makes `gd run`
# stop as soon as the engine is up (see main.go).
engine=graphics/musl_amd64.editor
if [ ! -x "$engine" ] || [ -n "$(find . main.go internal -name '*.go' -newer "$engine" -not -path './.git/*' -print -quit)" ]; then
  echo "engine is out of date, rebuilding (gd run)..."
  if ! AVIARY_BUILD_ONLY=1 gd run > /tmp/aviary-devshot-build.log 2>&1; then
    tail -20 /tmp/aviary-devshot-build.log
    echo "build failed"
    exit 1
  fi
fi
export DEVSHOT_ENGINE=$(realpath "$engine")

jobs_max=${DEVSHOT_JOBS:-$(( $(nproc 2>/dev/null || echo 4) / 4 ))}
[ -n "${DEVSHOT_SHOT:-}" ] && jobs_max=1
[ "$jobs_max" -lt 1 ] && jobs_max=1

work=$(mktemp -d -t aviary-devshot-XXXXXX)
trap 'rm -rf "$work"' EXIT

names=()
pids=()
for name in "${SCENARIOS[@]}"; do
  if [ $# -gt 0 ]; then
    printf '%s\n' "$@" | grep -qx "$name" || continue
  fi
  while [ "$(jobs -rp | wc -l)" -ge "$jobs_max" ]; do sleep 0.2; done
  "$0" --one "$name" > "$work/$name.log" 2>&1 &
  pids+=($!)
  names+=("$name")
done

# Reported in the order listed, not the order they finished, so two runs of the
# suite read the same.
failed=()
for i in "${!pids[@]}"; do
  wait "${pids[$i]}" || failed+=("${names[$i]}")
  cat "$work/${names[$i]}.log"
done

[ -n "${DEVSHOT_SHOT:-}" ] && echo && echo "screenshots in $DEVSHOT_OUT"
if [ ${#failed[@]} -gt 0 ]; then
  echo
  echo "failed: ${failed[*]}"
  exit 1
fi
echo
echo "all scenarios passed"
