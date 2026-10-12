#!/bin/bash
P=/tmp/claude-1000/-home-johnandersen777-src-publicdomainrelay-kcp/90fa59d8-02f3-4261-b817-d037e7405f23/scratchpad/linux-proofs
T=$P/bin/tapexec
U=swproof-vm-3.service
st() { systemctl --user show "$1" -p ActiveState -p SubState -p MainPID -p InvocationID -p Result --value | tr '\n' ' '; echo; }
startvm() {
  local final=$1
  local inner="ip -br link show tap0; exec $final"
  $P/bin/startvm -unit $U -p Restart=no -p KillMode=control-group -p MemorySwapMax=0 -- \
    /usr/bin/pasta --config-net -q -f -a 192.168.100.2 -n 255.255.255.0 -g 192.168.100.1 --dns-forward 192.168.100.1 \
    -- /bin/sh -c 'exec "$0" tap0 /bin/sh -c "$1"' $T "$inner"
  sleep 1.5
}
child() { pgrep -P $(systemctl --user show $U -p MainPID --value) | head -1; }
gone() { for p in "$@"; do kill -0 $p 2>/dev/null && echo "pid $p ALIVE" || echo "pid $p gone"; done; }

echo "== $(date -u +%FT%TZ) E4b-1 pasta+tap+exec inside user unit $U (final=tapexec attach tap0)"
startvm "$T attach tap0"
echo "unit: $(st $U)"
systemd-cgls --user-unit $U --no-pager 2>&1 | sed "s|$P|.|g" | cut -c1-150
MP=$(systemctl --user show $U -p MainPID --value); CH=$(child)
echo "MainPID=$MP ($(cat /proc/$MP/comm)) child=$CH ($(cat /proc/$CH/comm)) child-pidns=$(readlink /proc/$CH/ns/pid) host-pidns=$(readlink /proc/self/ns/pid) NSpid: $(grep NSpid /proc/$CH/status | tr '\t' ' ')"
journalctl --user -u $U -o cat --no-pager | grep -E 'tapexec|attach|tap0' | sed "s|$P|.|g" | tail -4
echo "-- identity of the VMM stand-in (child, not MainPID) via GetUnitByPIDFD:"
INV=$(systemctl --user show $U -p InvocationID --value)
$P/bin/ident -mode private -pid $CH -unit $U -inv $INV | grep -E 'GetUnitByPIDFD|verdict'

echo "== $(date -u +%FT%TZ) E4b-2 bidder unit restart/kill/stop survival with $U"
B=swproof-bidder-e4
BASE=$(st $U)
systemd-run --user -q --unit=$B -p Type=exec $P/bin/bidder -mode private -unit $U -children=false
sleep 1
systemctl --user restart $B; sleep 1; [ "$(st $U)" = "$BASE" ] && echo "restart: PASS" || echo "restart: FAIL $(st $U)"
kill -9 $(systemctl --user show $B.service -p MainPID --value); sleep 1; [ "$(st $U)" = "$BASE" ] && echo "kill -9: PASS" || echo "kill -9: FAIL"
systemctl --user start $B.service; sleep 1
systemctl --user stop $B; sleep 1; [ "$(st $U)" = "$BASE" ] && echo "stop: PASS" || echo "stop: FAIL"
systemctl --user reset-failed $B.service 2>/dev/null
gone $CH

echo "== $(date -u +%FT%TZ) E4b-3 kill -9 pasta (MainPID $MP) inside unit -> does systemd reap the VMM stand-in $CH?"
kill -9 $MP; sleep 1
echo "unit: $(systemctl --user show $U -p LoadState -p ActiveState -p Result -p ExecMainCode -p ExecMainStatus --value | tr '\n' ' ')"
gone $MP $CH
systemctl --user reset-failed $U 2>/dev/null

echo "== $(date -u +%FT%TZ) E4b-4 kill -9 the VMM stand-in -> pasta exits -> unit?"
startvm "$T attach tap0"; MP=$(systemctl --user show $U -p MainPID --value); CH=$(child)
kill -9 $CH; sleep 1
echo "unit: $(systemctl --user show $U -p LoadState -p ActiveState -p Result -p ExecMainCode -p ExecMainStatus --value | tr '\n' ' ')"
gone $MP $CH
systemctl --user reset-failed $U 2>/dev/null

for final in "$T attach tap0" "/usr/bin/sleep infinity"; do
  echo "== $(date -u +%FT%TZ) E4b-5 systemctl stop latency, final=[$final] (pid-ns init signal semantics)"
  startvm "$final"; MP=$(systemctl --user show $U -p MainPID --value); CH=$(child)
  grep -E 'SigCgt' /proc/$CH/status | sed 's/^/child /'
  t0=$(date +%s.%N); timeout 15 systemctl --user stop $U; rc=$?; t1=$(date +%s.%N)
  echo "stop rc=$rc took=$(echo "$t1 - $t0" | bc)s"
  gone $MP $CH
  journalctl --user -u $U -o cat --no-pager | tail -3
  if kill -0 $CH 2>/dev/null; then systemctl --user kill -s KILL $U; sleep 0.5; fi
  systemctl --user reset-failed $U 2>/dev/null
done
echo "leftovers: $(systemctl --user list-units --all 'swproof-*' --no-legend | wc -l) units; procs: $(pgrep -f "$T" | wc -l)"
