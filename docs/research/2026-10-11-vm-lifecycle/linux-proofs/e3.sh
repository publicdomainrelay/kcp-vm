#!/bin/bash
P=/tmp/claude-1000/-home-johnandersen777-src-publicdomainrelay-kcp/90fa59d8-02f3-4261-b817-d037e7405f23/scratchpad/linux-proofs
U=swproof-vm-3id.service; cd $P
echo "== $(date -u +%FT%TZ) E3 identity CLK_TCK=$(getconf CLK_TCK)"
$P/bin/startvm -unit $U -p Restart=no -- /usr/bin/sleep infinity
MP=$(systemctl --user show $U -p MainPID --value); INV=$(systemctl --user show $U -p InvocationID --value)
echo "systemctl: MainPID=$MP InvocationID=$INV"
echo "+ busctl --user call org.freedesktop.systemd1 /org/freedesktop/systemd1 org.freedesktop.systemd1.Manager GetUnitByPID u $MP"
busctl --user call org.freedesktop.systemd1 /org/freedesktop/systemd1 org.freedesktop.systemd1.Manager GetUnitByPID u $MP
echo "+ busctl --user introspect ... | grep PIDFD"
busctl --user introspect org.freedesktop.systemd1 /org/freedesktop/systemd1 org.freedesktop.systemd1.Manager | grep -i pidfd
for m in private session; do
  echo "---- ident via $m"
  $P/bin/ident -mode $m -pid $MP -unit $U -inv $INV
done
T=$(awk '{s=$0; sub(/.*\) /,"",s); split(s,f," "); print f[20]}' /proc/$MP/stat)
echo "learned ticks=$T"
echo "---- wrong ticks (simulated pid reuse)"
$P/bin/ident -mode private -pid $MP -unit $U -inv $INV -ticks $((T+1))
echo "---- bus unreachable: nonexistent socket"
$P/bin/ident -mode private -bus nope.sock -pid $MP -unit $U -inv $INV -ticks $T
echo "---- bus unreachable: stale socket file (no listener)"
python3 -c "import socket;s=socket.socket(socket.AF_UNIX);s.bind('stale.sock')"
$P/bin/ident -mode private -bus stale.sock -pid $MP -unit $U -inv $INV -ticks $T
rm -f stale.sock
echo "---- bus hung: listener that never answers (2s ctx timeout)"
python3 -c "
import socket,time,os
p='hang.sock'
s=socket.socket(socket.AF_UNIX);s.bind(p);s.listen(5)
c=[]
t=time.time()
s.settimeout(1)
while time.time()-t<6:
  try: c.append(s.accept())
  except Exception: pass
os.unlink(p)
" &
HP=$!; sleep 0.5
timeout 10 $P/bin/ident -mode private -bus hang.sock -pid $MP -unit $U -inv $INV -ticks $T
wait $HP
echo "---- hold pidfd, stop the unit during hold, recheck (stale pidfd)"
( sleep 1; systemctl --user stop $U ) &
$P/bin/ident -mode private -pid $MP -unit $U -inv $INV -ticks $T -hold 2s
wait
echo "---- after stop, fresh check of old pid"
$P/bin/ident -mode private -pid $MP -unit $U -inv $INV -ticks $T
systemctl --user reset-failed $U 2>/dev/null
