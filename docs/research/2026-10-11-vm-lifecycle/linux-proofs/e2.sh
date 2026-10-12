#!/bin/bash
P=/tmp/claude-1000/-home-johnandersen777-src-publicdomainrelay-kcp/90fa59d8-02f3-4261-b817-d037e7405f23/scratchpad/linux-proofs
SV=$P/bin/startvm
U=swproof-vm-2.service
CG=/sys/fs/cgroup/user.slice/user-1000.slice/user@1000.service/app.slice
echo "== $(date -u +%FT%TZ) E2a properties accepted + read back"
echo "+ startvm -unit $U -p Restart=no -p KillMode=control-group -p OOMPolicy=stop -p MemoryMax=268435456 -p CPUQuotaPerSecUSec=200000 -p TasksMax=64 -p ManagedOOMPreference=avoid -p CollectMode=inactive-or-failed -p IOWeight=50 -p AllowedCPUs=1 -- /usr/bin/sleep infinity"
$SV -unit $U -p Restart=no -p KillMode=control-group -p OOMPolicy=stop -p MemoryMax=268435456 -p CPUQuotaPerSecUSec=200000 -p TasksMax=64 -p ManagedOOMPreference=avoid -p CollectMode=inactive-or-failed -p IOWeight=50 -p AllowedCPUs=1 -- /usr/bin/sleep infinity
systemctl --user show $U -p Restart -p KillMode -p OOMPolicy -p MemoryMax -p CPUQuotaPerSecUSec -p TasksMax -p ManagedOOMPreference -p CollectMode -p IOWeight -p AllowedCPUs -p ControlGroup -p MainPID -p InvocationID -p Slice
echo "-- kernel cgroup files in $CG/$U"
for f in memory.max cpu.max pids.max io.weight io.bfq.weight cpuset.cpus cpuset.cpus.effective cgroup.controllers; do printf '%s: ' $f; cat $CG/$U/$f 2>&1 | head -1; done
echo "-- parent app.slice subtree_control: $(cat $CG/cgroup.subtree_control)"
echo "-- ManagedOOMPreference note: oomd honors avoid/omit only if cgroup owned by root or user.oomd xattr; xattrs on cgroup dir:"
getfattr -d -m - $CG/$U 2>&1 | head -5
MP=$(systemctl --user show $U -p MainPID --value)
echo "-- actual cpu affinity of main pid: $(taskset -pc $MP 2>&1)"
echo "== $(date -u +%FT%TZ) E2b kill -9 main pid $MP (Restart=no, CollectMode=inactive-or-failed)"
kill -9 $MP; sleep 1
echo "after kill: $(systemctl --user show $U -p LoadState -p ActiveState -p Result -p ExecMainStatus -p ExecMainCode -p InvocationID -p MainPID --value | tr '\n' ' ')"
echo "list-units: $(systemctl --user list-units --all $U --no-legend | head -1)"
echo "journal tail:"; journalctl --user -u $U -o short-iso-precise --no-pager | tail -4

echo "== $(date -u +%FT%TZ) E2b' same with CollectMode=inactive (default)"
$SV -unit swproof-vm-2b.service -p Restart=no -- /usr/bin/sleep infinity
MP=$(systemctl --user show swproof-vm-2b.service -p MainPID --value); kill -9 $MP; sleep 1
echo "after kill: $(systemctl --user show swproof-vm-2b.service -p LoadState -p ActiveState -p Result -p ExecMainStatus -p ExecMainCode -p InvocationID --value | tr '\n' ' ')"
systemctl --user reset-failed swproof-vm-2b.service
echo "after reset-failed: $(systemctl --user show swproof-vm-2b.service -p LoadState -p ActiveState --value | tr '\n' ' ')"

echo "== $(date -u +%FT%TZ) E2c memory enforcement: MemoryMax=64M, OOMPolicy=stop, program allocs 512M"
$SV -unit swproof-vm-2m.service -p MemoryMax=67108864 -p OOMPolicy=stop -p Restart=no -- /usr/bin/python3 -c "import time; b=bytearray(512*1024*1024); time.sleep(30)"
sleep 3
echo "result: $(systemctl --user show swproof-vm-2m.service -p ActiveState -p Result -p ExecMainStatus -p ExecMainCode --value | tr '\n' ' ')"
journalctl --user -u swproof-vm-2m.service -o cat --no-pager | tail -3
systemctl --user reset-failed swproof-vm-2m.service 2>/dev/null; systemctl --user stop swproof-vm-2m.service 2>/dev/null

echo "== $(date -u +%FT%TZ) E2d pids enforcement: TasksMax=4, sh forks 10 sleeps"
$SV -unit swproof-vm-2p.service -p TasksMax=4 -p Restart=no -- /bin/sh -c 'for i in 1 2 3 4 5 6 7 8 9 10; do sleep 30 & done; sleep 1; echo forked; jobs | wc -l; sleep 30'
sleep 2
echo "pids.current=$(cat $CG/swproof-vm-2p.service/pids.current 2>&1) pids.max=$(cat $CG/swproof-vm-2p.service/pids.max 2>&1)"
journalctl --user -u swproof-vm-2p.service -o cat --no-pager | grep -iE 'fork|resource|forked|^[0-9]+$' | sort | uniq -c | head -5
systemctl --user stop swproof-vm-2p.service; systemctl --user reset-failed swproof-vm-2p.service 2>/dev/null

echo "== $(date -u +%FT%TZ) E2e cpu enforcement: CPUQuota=20% busy loop 3s"
$SV -unit swproof-vm-2c.service -p CPUQuotaPerSecUSec=200000 -p Restart=no -- /bin/sh -c 'while :; do :; done'
sleep 3
cat $CG/swproof-vm-2c.service/cpu.stat 2>&1 | grep -E 'usage_usec|nr_throttled|throttled_usec'
echo "cpu.max=$(cat $CG/swproof-vm-2c.service/cpu.max)"
systemctl --user stop swproof-vm-2c.service; systemctl --user reset-failed swproof-vm-2c.service 2>/dev/null
echo "== leftovers: $(systemctl --user list-units --all 'swproof-*' --no-legend | wc -l)"
