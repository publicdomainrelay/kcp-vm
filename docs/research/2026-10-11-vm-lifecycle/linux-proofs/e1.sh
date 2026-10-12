#!/bin/bash
P=/tmp/claude-1000/-home-johnandersen777-src-publicdomainrelay-kcp/90fa59d8-02f3-4261-b817-d037e7405f23/scratchpad/linux-proofs
MODE=${1:-session}
VM=swproof-vm-1-$MODE.service
B=swproof-bidder-$MODE
st() { systemctl --user show "$1" -p ActiveState -p SubState -p MainPID -p InvocationID -p Result --value | tr '\n' ' '; echo; }
alive() { for p in "$@"; do if kill -0 "$p" 2>/dev/null; then echo "pid $p ALIVE ($(ps -o cgroup= -p $p | sed 's/.*::\///'))"; else echo "pid $p DEAD"; fi; done; }
echo "== $(date -u +%FT%TZ) MODE=$MODE"
echo "+ systemd-run --user --unit=$B -p Type=exec $P/bin/bidder -mode $MODE -unit $VM"
systemd-run --user --unit=$B -p Type=exec $P/bin/bidder -mode $MODE -unit $VM
for i in $(seq 1 30); do systemctl --user is-active -q $VM && break; sleep 0.2; done
sleep 1
journalctl --user -u $B -o cat --no-pager | tail -6
PD=$(journalctl --user -u $B -o cat --no-pager | sed -n 's/.*setsid+pdeathsig pid=//p' | tail -1)
SS=$(journalctl --user -u $B -o cat --no-pager | sed -n 's/.*setsid-only pid=//p' | tail -1)
BASE=$(st $VM); echo "vm baseline: $BASE"
echo "bidder: $(st $B.service)"
echo "vm cgroup: $(systemctl --user show $VM -p ControlGroup --value)  bidder cgroup: $(systemctl --user show $B.service -p ControlGroup --value)"
alive $PD $SS

echo "== $(date -u +%FT%TZ) step1: systemctl --user restart $B"
systemctl --user restart $B; sleep 1.5
NOW=$(st $VM); echo "vm after restart: $NOW"; [ "$NOW" = "$BASE" ] && echo "STEP1 PASS vm unchanged" || echo "STEP1 FAIL"
echo "bidder: $(st $B.service)"
journalctl --user -u $B -o cat --no-pager | grep -E 'StartTransient|bidder pid' | tail -2
echo "plain children of FIRST bidder instance:"; alive $PD $SS

PD2=$(journalctl --user -u $B -o cat --no-pager | sed -n 's/.*setsid+pdeathsig pid=//p' | tail -1)
SS2=$(journalctl --user -u $B -o cat --no-pager | sed -n 's/.*setsid-only pid=//p' | tail -1)
MP=$(systemctl --user show $B.service -p MainPID --value)
echo "== $(date -u +%FT%TZ) step2: kill -9 bidder MainPID=$MP"
kill -9 $MP; sleep 1.5
NOW=$(st $VM); echo "vm after kill -9: $NOW"; [ "$NOW" = "$BASE" ] && echo "STEP2 PASS vm unchanged" || echo "STEP2 FAIL"
echo "bidder: $(st $B.service)"
echo "plain children of second bidder (pdeathsig, setsid-only):"; alive $PD2 $SS2

echo "+ restart bidder for step3"
systemctl --user start $B.service 2>&1 || { systemctl --user reset-failed $B.service; systemd-run --user --unit=$B -p Type=exec $P/bin/bidder -mode $MODE -unit $VM; }
sleep 1.5
echo "bidder: $(st $B.service)"
PD3=$(journalctl --user -u $B -o cat --no-pager | sed -n 's/.*setsid+pdeathsig pid=//p' | tail -1)
SS3=$(journalctl --user -u $B -o cat --no-pager | sed -n 's/.*setsid-only pid=//p' | tail -1)
alive $PD3 $SS3
echo "== $(date -u +%FT%TZ) step3: systemctl --user stop $B"
systemctl --user stop $B; sleep 1
NOW=$(st $VM); echo "vm after stop: $NOW"; [ "$NOW" = "$BASE" ] && echo "STEP3 PASS vm unchanged" || echo "STEP3 FAIL"
echo "bidder: $(st $B.service)"
echo "plain children of third bidder after stop:"; alive $PD3 $SS3
