#!/usr/bin/env bash
set -uo pipefail

# Records the first RFP-flow guest of a bed whose VMM runs under
# swc-userns-stage: the unit's processes, each one's host uid and uid_map, the
# work directory's ACL, and the AVC denials since the capture began.
#
# Usage: capture.sh <fc-work-root> <account> <outfile>

ROOT=${1:?work root}
ACCOUNT=${2:?account}
OUT=${3:?outfile}
UID_A=$(id -u "$ACCOUNT")
T0=$(date +%H:%M:%S)
sc() { sudo -n -u "$ACCOUNT" env XDG_RUNTIME_DIR="/run/user/$UID_A" systemctl --user "$@"; }

while :; do
  for d in "$ROOT"/fc-*; do
    [ -s "$d/result.json" ] || continue
    name=$(basename "$d")
    unit=socialweb-vm-$name.service
    {
      echo "== $(date -u +%Y-%m-%dT%H:%M:%SZ) guest $name unit $unit (user manager of $ACCOUNT)"
      sc show -p ActiveState,MainPID,ControlGroup,ExecMainStartTimestamp "$unit"
      cg=$(sc show -p ControlGroup --value "$unit")
      echo "-- processes in the unit's cgroup: pid ppid hostuid comm | uid_map as seen from the host"
      for p in $(cat "/sys/fs/cgroup$cg/cgroup.procs"); do
        printf '%s | %s\n' "$(ps -o pid=,ppid=,uid=,comm= -p "$p")" "$(tr -s ' ' <"/proc/$p/uid_map" | tr '\n' ';')"
      done
      echo "-- result.json"; cat "$d/result.json"
      echo "-- work dir ACL"; getfacl -p "$d" 2>/dev/null | grep -v '^#'
      echo "-- work files"; ls -ln "$d"
      vmm=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["vmm_pid"])' "$d/result.json")
      echo "-- VMM pid $vmm: exe=$(sudo -n readlink "/proc/$vmm/exe") fds:"
      sudo -n ls -l "/proc/$vmm/fd" | awk '{print $NF}' | grep -E 'kvm|tun|sock' | sort | uniq -c
      echo "-- VMM uid owns, outside the work dir:"
      vuid=$(stat -c %u "/proc/$vmm")
      sudo -n timeout 600 find / /home /var /opt -xdev -path "$ROOT" -prune -o -uid "$vuid" -print 2>/dev/null | head -5
      echo "-- AVC since $T0"
      sudo -n ausearch -m avc,user_avc -ts "$T0" 2>&1 | grep -v '^----' | head -10
    } >"$OUT" 2>&1
    exit 0
  done
  sleep 2
done
