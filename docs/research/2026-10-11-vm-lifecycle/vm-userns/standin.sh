#!/usr/bin/env bash
set -uo pipefail

# Stand-in readings for M0-VM-USERNS. Runs as the account that owns a subuid
# range (RANGE_USER, default test-0001, because the invoking user has none) in
# that account's user manager and as a plain process group.
#
#   candidates: A = swc-userns-stage (static, single-threaded)
#               B = util-linux unshare --map-users/--map-groups --setuid/--setgid
#               B2 = unshare --keep-caps then setpriv --keep-groups (keeps supplementary groups)
#   Every candidate maps inner 0..SLOT-1 and switches to inner id INNER (not 0):
#   pasta confined as pasta_t cannot write its own namespace's uid_map when it
#   starts as root of the outer namespace.
#               G = a Go process calling unshare(CLONE_NEWUSER) itself
#
# Usage: standin.sh <outdir>

OUT=${1:?outdir}
BIN=${BIN:-/var/tmp/swc-userns-spike/bin}
RANGE_USER=${RANGE_USER:-test-0001}
RU_UID=$(id -u "$RANGE_USER")
RU_RT=/run/user/$RU_UID
BASE=$(awk -F: -v u="$RANGE_USER" '$1==u {print $2}' /etc/subuid)
GBASE=$(awk -F: -v u="$RANGE_USER" '$1==u {print $2}' /etc/subgid)
SPAN=$(awk -F: -v u="$RANGE_USER" '$1==u {print $3}' /etc/subuid)
SLOT=${SLOT:-$(( SPAN / 2 ))}
PASTA=${PASTA:-/usr/bin/pasta}
INNER=${INNER:-1000}
PROBES="/home/johnandersen777/.ssh/config /home/johnandersen777/.local/state/swc-tasks /etc/shadow"
mkdir -p "$OUT"
chmod 1777 "$OUT"

ru() { sudo -n -u "$RANGE_USER" env XDG_RUNTIME_DIR="$RU_RT" DBUS_SESSION_BUS_ADDRESS="unix:path=$RU_RT/bus" "$@"; }
now() { date -u +%Y-%m-%dT%H:%M:%S.%3NZ; }

pasta_argv() {
  PA=("$PASTA" --config-net -q -f -a 172.30.0.0 -n 255.255.255.252 -g 172.30.0.1 --dns-forward 172.30.0.1
    /bin/sh -c 'exec "$0" "$@"' "$BIN/standin" "$1" $PROBES)
}

stage_argv() {
  local cand=$1 k=$2
  local ub=$(( BASE + k * SLOT )) gb=$(( GBASE + k * SLOT ))
  case "$cand" in
    A) echo "$BIN/swc-userns-stage" "$ub" "$gb" "$SLOT" "$INNER" ;;
    B) echo /usr/bin/unshare --user --map-users="0:$ub:$SLOT" --map-groups="0:$gb:$SLOT" --setuid "$INNER" --setgid "$INNER" -- ;;
    B2) echo /usr/bin/unshare --user --map-users="0:$ub:$SLOT" --map-groups="0:$gb:$SLOT" --keep-caps -- /usr/bin/setpriv --reuid "$INNER" --regid "$INNER" --keep-groups -- ;;
  esac
}

wait_ready() {
  local f=$1
  for _ in $(seq 1 100); do
    grep -q '^ready=' "$f" 2>/dev/null && return 0
    sleep 0.1
  done
  return 1
}

tree_of_cgroup() {
  local cg=$1
  for p in $(cat "/sys/fs/cgroup$cg/cgroup.procs" 2>/dev/null); do
    ps -o pid=,ppid=,user:12=,comm= -p "$p"
  done
}

unit_run() {
  local cand=$1 k=$2 name=socialweb-vm-spike-$1-$2
  local log=$OUT/unit-$cand-$k.txt rd=$OUT/unit-$cand-$k.out
  rm -f "$rd"
  pasta_argv "$rd"
  local t0 t1
  t0=$(date +%s%N)
  ru systemd-run --user --quiet --collect --unit "$name" -p Type=exec -p KillMode=control-group -p KillSignal=SIGKILL \
    -p TimeoutStopSec=10 -p "StandardOutput=append:$rd.err" -p "StandardError=append:$rd.err" \
    $(stage_argv "$cand" "$k") "${PA[@]}"
  wait_ready "$rd"
  t1=$(date +%s%N)
  {
    echo "== candidate $cand slot $k unit $name (systemd-user of $RANGE_USER) $(now)"
    echo "start_to_ready_ms=$(( (t1 - t0) / 1000000 ))"
    cat "$rd"
    local cg
    cg=$(ru systemctl --user show -p ControlGroup --value "$name")
    echo "cgroup=$cg"
    echo "-- unit processes (pid ppid user comm)"
    tree_of_cgroup "$cg"
    local vpid
    vpid=$(awk -F= '$1=="pid"{print $2}' "$rd")
    local hpid
    hpid=$(for p in $(cat "/sys/fs/cgroup$cg/cgroup.procs"); do [ "$(sudo -n readlink /proc/$p/exe 2>/dev/null)" = "$BIN/standin" ] && echo "$p"; done | head -1)
    echo "standin_host_pid=$hpid inner_pid=$vpid"
    echo "host_uid_of_standin=$(stat -c %u /proc/$hpid 2>/dev/null)"
    echo "-- tap in pasta's netns"
    sudo -n nsenter -t "$hpid" -n ip -d -br link show fctap0 2>&1
    sudo -n nsenter -t "$hpid" -n ip -d link show fctap0 2>&1 | grep -o 'tun type tap.*' || true
  } >"$log" 2>&1
  echo "$name"
}

unit_stop() {
  local name=$1
  ru systemctl --user stop "$name"
  ru systemctl --user is-active "$name" 2>/dev/null || true
}

proc_run() {
  local cand=$1 k=$2
  local log=$OUT/proc-$cand-$k.txt rd=$OUT/proc-$cand-$k.out
  rm -f "$rd"
  pasta_argv "$rd"
  local t0 t1
  t0=$(date +%s%N)
  sudo -n -u "$RANGE_USER" setsid $(stage_argv "$cand" "$k") "${PA[@]}" >>"$rd.err" 2>&1 </dev/null &
  wait_ready "$rd"
  t1=$(date +%s%N)
  local hpid pg
  hpid=$(pgrep -f -n -x "$BIN/standin $rd $PROBES")
  pg=$(ps -o pgid= -p "$hpid" | tr -d ' ')
  {
    echo "== candidate $cand slot $k process group (proc backend stand-in) $(now)"
    echo "start_to_ready_ms=$(( (t1 - t0) / 1000000 ))"
    cat "$rd"
    echo "-- process group $pg (pid ppid user comm)"
    ps -o pid=,ppid=,user:12=,comm= -g "$pg"
    echo "host_uid_of_standin=$(stat -c %u /proc/$hpid)"
  } >"$log" 2>&1
  sudo -n kill -KILL -- "-$pg"
}

AVC_T0=$(date +%H:%M:%S)
{
  echo "host=$(hostname) $(uname -r) selinux=$(getenforce) $(now)"
  echo "range_user=$RANGE_USER uid=$RU_UID subuid=$BASE:$SPAN subgid=$GBASE slot_size=$SLOT"
  echo "invoking_user=$(id -un) subuid_entry=$(grep -c "^$(id -un):" /etc/subuid)"
  ls -l /dev/kvm /dev/net/tun /usr/bin/newuidmap /usr/bin/newgidmap
  getcap /usr/bin/newuidmap /usr/bin/newgidmap
  /usr/bin/unshare --version
  $PASTA --version | head -1
  echo "-- G: Go process unshare(CLONE_NEWUSER)"
  ru "$BIN/gounshare"
  echo "-- stage size $(stat -c %s "$BIN/swc-userns-stage") bytes sha256 $(sha256sum "$BIN/swc-userns-stage" | cut -c1-16)"
} >"$OUT/host.txt" 2>&1

for cand in A B B2; do
  u0=$(unit_run "$cand" 0)
  u1=$(unit_run "$cand" 1)
  unit_stop "$u0"; unit_stop "$u1"
  proc_run "$cand" 0
done

{
  echo "-- added latency: 200 x exec /bin/true, direct vs through each stage (as $RANGE_USER)"
  for cand in direct A B B2; do
    if [ "$cand" = direct ]; then argv=""; else argv=$(stage_argv "$cand" 0); fi
    ms=$(ru bash -c "t0=\$(date +%s%N); for i in \$(seq 200); do $argv /bin/true || exit 9; done; echo \$(( (\$(date +%s%N) - t0) / 200000 ))")
    echo "$cand per_exec_us=$ms"
  done
} >"$OUT/latency.txt" 2>&1

{
  echo "-- files owned by the slot uids on this host (expect none)"
  for k in 0 1; do
    u=$(( BASE + k * SLOT + INNER ))
    n=$(sudo -n timeout 600 find / /home /var /tmp /opt -xdev -path "$OUT" -prune -o -uid "$u" -print 2>/dev/null | head -5)
    echo "uid $u: ${n:-none}"
  done
  echo "-- AVC denials since $AVC_T0"
  sudo -n ausearch -m avc,user_avc -ts "$AVC_T0" 2>&1 | grep -v '^----' | head -20
} >"$OUT/owners-avc.txt" 2>&1
echo "readings in $OUT"
