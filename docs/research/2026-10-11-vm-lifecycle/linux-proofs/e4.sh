#!/bin/bash
P=/tmp/claude-1000/-home-johnandersen777-src-publicdomainrelay-kcp/90fa59d8-02f3-4261-b817-d037e7405f23/scratchpad/linux-proofs
T=$P/bin/tapexec
INNER="ip -br link show tap0; ip -br addr; exec $T attach tap0"
tree() { ps -o pid,ppid,pgid,sid,user,comm,args --forest -g "$1" 2>/dev/null | cut -c1-200; }
desc() { local p=$1; echo "pid $p exe=$(readlink /proc/$p/exe 2>&1) comm=$(cat /proc/$p/comm 2>&1) netns=$(readlink /proc/$p/ns/net 2>&1) userns=$(readlink /proc/$p/ns/user 2>&1)"; }

echo "== $(date -u +%FT%TZ) E4a: pasta + tapexec + execve, interactive (no systemd unit)"
echo "+ pasta --config-net -q -f -a 192.168.100.2 -n 255.255.255.0 -g 192.168.100.1 --dns-forward 192.168.100.1 -- /bin/sh -c 'exec \"\$0\" tap0 /bin/sh -c \"\$1\"' $T '$INNER'"
setsid pasta --config-net -q -f -a 192.168.100.2 -n 255.255.255.0 -g 192.168.100.1 --dns-forward 192.168.100.1 -- /bin/sh -c 'exec "$0" tap0 /bin/sh -c "$1"' $T "$INNER" > $P/e4a.out 2>&1 &
PP=$!
sleep 1.5
echo "--- output:"; cat $P/e4a.out
echo "--- process tree (session of pasta $PP):"
ps -o pid,ppid,comm,args --forest --ppid $PP -p $PP | cut -c1-160
CH=$(pgrep -P $PP)
echo "pasta pid=$PP children=[$CH]"
desc $PP; for c in $CH; do desc $c; echo "grandchildren of $c: [$(pgrep -P $c)]"; done
echo "host netns: $(readlink /proc/self/ns/net)"
echo "--- tap seen from inside ns via nsenter --preserve-credentials -U -n -t $CH:"
nsenter --preserve-credentials -U -n -t $CH ip -d -br link show tap0 2>&1
nsenter --preserve-credentials -U -n -t $CH ip -d link show tap0 2>&1 | grep -o 'tun type tap.*'
echo "--- fds of exec'd process (tun fd from tapexec must not leak; attach fd present):"
ls -l /proc/$CH/fd 2>&1 | awk '{print $9,$10,$11}' | grep -v '^ *$'
echo "== $(date -u +%FT%TZ) kill -TERM pasta $PP"
kill -TERM $PP; sleep 1
for p in $PP $CH; do kill -0 $p 2>/dev/null && echo "pid $p STILL ALIVE" || echo "pid $p gone"; done
pkill -f "$T attach" && echo "cleanup: had to kill leftover attach"

echo "== $(date -u +%FT%TZ) E4a': kill -9 of pasta"
setsid pasta --config-net -q -f -a 192.168.100.2 -n 255.255.255.0 -g 192.168.100.1 -- /bin/sh -c 'exec "$0" tap0 /bin/sh -c "$1"' $T "$INNER" > $P/e4a2.out 2>&1 &
PP=$!; sleep 1.5; CH=$(pgrep -P $PP); echo "pasta=$PP child=$CH"
kill -9 $PP; sleep 1
for p in $PP $CH; do kill -0 $p 2>/dev/null && echo "pid $p STILL ALIVE ($(cat /proc/$p/status | grep PPid))" || echo "pid $p gone"; done
pkill -f "$T attach" && echo "cleanup: had to kill leftover attach"

echo "== $(date -u +%FT%TZ) E4a'': kill -9 of the exec'd child (VMM stand-in) -> pasta exits?"
setsid pasta --config-net -q -f -a 192.168.100.2 -n 255.255.255.0 -g 192.168.100.1 -- /bin/sh -c 'exec "$0" tap0 /bin/sh -c "$1"' $T "$INNER" > $P/e4a3.out 2>&1 &
PP=$!; sleep 1.5; CH=$(pgrep -P $PP); echo "pasta=$PP child=$CH"
kill -9 $CH; sleep 1
kill -0 $PP 2>/dev/null && echo "pasta $PP STILL ALIVE" || { wait $PP; echo "pasta $PP exited rc=$?"; }
