#!/bin/bash
P=/tmp/claude-1000/-home-johnandersen777-src-publicdomainrelay-kcp/90fa59d8-02f3-4261-b817-d037e7405f23/scratchpad/linux-proofs
F=/var/tmp/m3live-k8s-tree/hono-compute-provider/hono-firecracker-standalone/firecracker
mkdir -p $P/fcrun && cd $P/fcrun
echo "== $(date -u +%FT%TZ) E5 firecracker non-boot checks ($($F --version | head -1))"
echo "--- E5a --no-api without --config-file"
$F --no-api 2>&1 | tail -2; echo "rc=$?"
echo "--- E5b invalid --id"
$F --id 'bad_id!' --api-sock a.sock 2>&1 | grep -o 'Invalid instance ID.*' | head -1
$F --id "$(printf 'a%.0s' $(seq 1 65))" --api-sock a.sock 2>&1 | grep -o 'Invalid instance ID.*' | head -1
echo "--- E5c api-sock path > 107 bytes"
LONG=$P/fcrun/$(printf 'x%.0s' $(seq 1 40)).sock; echo "len=${#LONG}"
timeout 3 $F --api-sock $LONG --id swproof-long 2>&1 | tail -2 | cut -c1-250; echo "rc=${PIPESTATUS[0]}"
echo "--- E5d pre-boot API only (no guest configured or started): GET / and GET /machine-config"
rm -f a.sock
$F --api-sock a.sock --id swproof-e5 > fc-e5d.log 2>&1 &
FP=$!
for i in $(seq 1 20); do [ -S a.sock ] && break; sleep 0.1; done
curl -s --unix-socket a.sock http://localhost/; echo
curl -s --unix-socket a.sock http://localhost/machine-config; echo
curl -s --unix-socket a.sock http://localhost/version; echo
echo "SendCtrlAltDel pre-boot:"; curl -s -X PUT --unix-socket a.sock http://localhost/actions -d '{"action_type":"SendCtrlAltDel"}'; echo
echo "SIGTERM handling (SigCgt bitmask; bit 14 = SIGTERM): $(grep SigCgt /proc/$FP/status)"
kill -TERM $FP; sleep 0.5; kill -0 $FP 2>/dev/null && echo "after SIGTERM: ALIVE?" || { wait $FP; echo "after SIGTERM: exited rc=$? (default action; not pid-ns init here)"; }
echo "log tail:"; tail -3 fc-e5d.log | cut -c1-200
echo "--- E5e api-sock path already exists (stale socket file)"
python3 -c "import socket;s=socket.socket(socket.AF_UNIX);s.bind('b.sock')"
timeout 3 $F --api-sock b.sock --id swproof-e5e 2>&1 | tail -2 | cut -c1-250; echo "rc=${PIPESTATUS[0]}"
rm -f b.sock a.sock
echo "--- E5f --config-file referencing missing kernel: parse/validate fails before any VM is created"
cat > cfg.json <<'EOF'
{"boot-source":{"kernel_image_path":"/nonexistent/vmlinux","boot_args":"console=ttyS0"},
 "drives":[],"machine-config":{"vcpu_count":1,"mem_size_mib":128},
 "network-interfaces":[{"iface_id":"eth0","host_dev_name":"tap0"}],
 "logger":{"log_path":"fc.log","level":"Info"},"metrics":{"metrics_path":"fc.metrics"}}
EOF
: > fc.log; : > fc.metrics
timeout 5 $F --api-sock c.sock --id swproof-e5f --config-file cfg.json 2>&1 | tail -3 | cut -c1-250; echo "rc=${PIPESTATUS[0]}"
echo "fc.log:"; tail -3 fc.log | cut -c1-200
rm -f c.sock
pgrep -f "$F" && echo LEFTOVER || echo "no firecracker left"
