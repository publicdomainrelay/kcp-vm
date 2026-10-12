#!/bin/bash
P=/tmp/claude-1000/-home-johnandersen777-src-publicdomainrelay-kcp/90fa59d8-02f3-4261-b817-d037e7405f23/scratchpad/linux-proofs
D=$P/e6; rm -rf $D; mkdir -p $D
cd $P/gosrc
run() {
  local mode=$1 action=$2
  go build -ldflags "-X main.build=OLD" -o $D/nodeboot ./cmd/exeprobe
  go build -ldflags "-X main.build=NEW" -o $D/nodeboot.new ./cmd/exeprobe
  rm -f $D/sig
  echo "== $(date -u +%FT%TZ) E6 REEXEC=$mode action=$action"
  echo "old inode=$(stat -c %i $D/nodeboot) new inode=$(stat -c %i $D/nodeboot.new)"
  SIGNAL_FILE=$D/sig REEXEC=$mode $D/nodeboot > $D/out.$mode.$action 2>&1 &
  local pid=$!
  sleep 0.5
  if [ "$action" = rename ]; then mv $D/nodeboot.new $D/nodeboot; else rm -f $D/nodeboot $D/nodeboot.new; fi
  echo "readlink /proc/$pid/exe now: $(readlink /proc/$pid/exe)"
  touch $D/sig; wait $pid
  sed "s|$P|.|g" $D/out.$mode.$action
}
run path rename
run procself rename
run path delete
run procself delete
