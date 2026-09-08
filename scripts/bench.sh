#!/usr/bin/env bash
# Benchmark gate against the fleet.
#
#   sched <label>   whole-run wall time per repo -- what scheduling changes move
#
# Results land in $OUT/sched-<label>.tsv so two runs can be diffed:
#
#   scripts/bench.sh sched before
#   ... change the scheduler ...
#   scripts/bench.sh sched after
#   scripts/bench.sh compare before after
#
# Timing is best-of-N after a warmup.
set -u

OUT=${GATE_BENCH_OUT:-/var/tmp/gate-bench}
REPOS=${GATE_BENCH_REPOS:-$(dirname "$0")/bench-repos.txt}
RUNS=${GATE_BENCH_RUNS:-3}
REPEAT_UNDER_MS=${GATE_BENCH_REPEAT_UNDER_MS:-5000}
TIMEOUT=${GATE_BENCH_TIMEOUT:-300}
export GOTMPDIR=/var/tmp TMPDIR=/var/tmp
mkdir -p "$OUT"

now() { date +%s%N; }
die() {
  echo "bench: $*" >&2
  exit 2
}

# best_of runs a command RUNS times and echoes the lowest wall time in ms. The
# minimum rather than the mean: a slow sample is contention with something else
# on the machine, and the floor is the number a change actually moves.
best_of() {
  local best=99999999 s e t
  local i=0
  while [ $((i += 1)) -le "$RUNS" ]; do
    s=$(now)
    eval "$1" >/dev/null 2>&1 </dev/null
    e=$(now)
    t=$(((e - s) / 1000000))
    [ "$t" -lt "$best" ] && best=$t
    # Repeats exist to see past scheduling noise, which is milliseconds. Once a
    # single run costs seconds the noise cannot change the verdict, and a gate
    # that hangs to its timeout would otherwise spend RUNS x TIMEOUT proving
    # what one sample already showed.
    [ "$t" -gt "$REPEAT_UNDER_MS" ] && break
  done
  echo "$best"
}

mode_sched() {
  local label=$1
  local f=$OUT/sched-$label.tsv
  local repo name ms_default ms_serial bytes code
  printf 'repo\tgates\tms_default\tms_serial\tbytes\texit\n' >"$f"
  while read -r repo; do
    case "$repo" in '' | '#'*) continue ;; esac
    [ -d "$repo" ] || {
      echo "bench: no such repo: $repo" >&2
      continue
    }
    name=$(basename "$repo")
    local n
    n=$(gate -C "$repo" --list 2>/dev/null | sed -n 's/^\([0-9]\+\) gate(s).*/\1/p')
    [ -n "${n:-}" ] || continue

    gate -C "$repo" --timeout "${TIMEOUT}s" >/dev/null 2>&1 </dev/null # warm
    ms_default=$(best_of "gate -C '$repo' --timeout ${TIMEOUT}s")
    ms_serial=$(best_of "gate -C '$repo' --timeout ${TIMEOUT}s --serial")

    # Output size and status come from one more run, so they describe the same
    # thing the timings do.
    gate -C "$repo" --timeout "${TIMEOUT}s" >"$OUT/.out" 2>&1 </dev/null
    code=$?
    bytes=$(wc -c <"$OUT/.out")

    printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$name" "$n" "$ms_default" "$ms_serial" "$bytes" "$code" >>"$f"
    printf '%-22s default=%7sms serial=%7sms\n' "$name" "$ms_default" "$ms_serial"
  done <"$REPOS"
  rm -f "$OUT/.out"
  echo "bench: wrote $f"
}

mode_compare() {
  local a=$1 b=$2
  local fa=$OUT/sched-$a.tsv
  local fb=$OUT/sched-$b.tsv
  [ -f "$fa" ] || die "missing $fa"
  [ -f "$fb" ] || die "missing $fb"
  join -t$'\t' -j1 <(tail -n +2 "$fa" | sort) <(tail -n +2 "$fb" | sort) |
    awk -F'\t' -v A="$a" -v B="$b" '
      BEGIN{printf "%-22s %10s %10s %9s %10s\n", "repo", A, B, "delta", "bytes"}
      {d=$8-$3; p=($3>0)?100*d/$3:0
       printf "%-22s %9sms %9sms %+8.1f%% %10s\n", $1, $3, $8, p, $10
       ta+=$3; tb+=$8}
      END{printf "%-22s %9dms %9dms %+8.1f%%\n", "TOTAL", ta, tb, 100*(tb-ta)/ta}'
}

case "${1:-}" in
  sched)
    [ $# -eq 2 ] || die "usage: bench.sh sched <label>"
    mode_sched "$2"
    ;;
  compare)
    [ $# -eq 3 ] || die "usage: bench.sh compare <a> <b>"
    mode_compare "$2" "$3"
    ;;
  *) die "usage: bench.sh sched <label> | compare <a> <b>" ;;
esac
