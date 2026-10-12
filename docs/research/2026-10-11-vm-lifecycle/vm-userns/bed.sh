#!/usr/bin/env bash
set -euo pipefail

# One guest rented through the RFP flow in this task's own market test bed,
# with the bidder's firecracker launch going through swc-userns-stage
# (requester -> RFP -> bid -> accept -> cloud-init user_data; nothing booted by
# hand). The VMM runs in a per-VM subuid user namespace in a transient unit of
# FC_USERNS_ACCOUNT's user manager. The invoking user has no subuid range on
# hat-8, so the range of an account that has one stands in for swc-bidder.
#
# Needs: eval "$(scripts/task-tree.ts env vm-userns)", FC_BIN holding
# socialweb-nodeboot (built from this tree), firecracker and swc-userns-stage.

HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/../../../.." && pwd)
FC_BIN=${FC_BIN:-$TASK_STATE_DIR/bin}

export BED_ID=${BED_ID:-vmuserns1}
export KEEP_ACCEPT_ROOT=${KEEP_ACCEPT_ROOT:-1}
export FC_NODEIMAGE=${FC_NODEIMAGE:-$HOME/kcp-image/socialweb-nodeimage}
export FC_CONFIG=${FC_CONFIG:-$HOME/kcp-image/nodeimage-market.json}
export FC_PREINSTALL=${FC_PREINSTALL:-$HOME/kcp-image/preinstall-market.json}
export FC_REPO_DIR=${FC_REPO_DIR:-$REPO}
export FC_RUNNER_IMAGE=${FC_RUNNER_IMAGE:-localhost/socialweb-firecracker-runner:local}
export FC_NODEBOOT=$FC_BIN/socialweb-nodeboot
export FC_VMM=$FC_BIN/firecracker
export FC_USERNS_STAGE=$FC_BIN/swc-userns-stage
export FC_USERNS_ACCOUNT=${FC_USERNS_ACCOUNT:-test-0001}
export FC_USERNS_SLOT_SIZE=${FC_USERNS_SLOT_SIZE:-32768}
export FC_USERNS_RUNTIME_DIR=${FC_USERNS_RUNTIME_DIR:-/var/tmp/swc-$BED_ID-rt}

exec bash "$REPO/deploy/examples/atproto/market/accept.sh"
