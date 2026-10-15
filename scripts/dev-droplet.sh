#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'EOF'
usage: scripts/dev-droplet.sh create [name] | setup [name] | push <publish-dir> [name] | swc [name] -- <socialweb-computer args>
       scripts/dev-droplet.sh it <test-regex> <publish-dir> [name] | ssh [name] [cmd...] | list | down
EOF
  exit 2
}

sizes="${DEV_DROPLET_SIZES:-c-16-intel c2-16vcpu-32gb-intel c-16 c2-16vcpu-32gb g-16vcpu-64gb-intel}"
regions="${DEV_DROPLET_REGIONS:-nyc3 nyc1 tor1 ams3 lon1 fra1 sfo3}"
image="${DEV_DROPLET_IMAGE:-ubuntu-24-04-x64}"
key_file="${DEV_DROPLET_KEY:-$HOME/.ssh/id_ed25519_no_password}"
state="$HOME/.tmp/dev-droplet"
guest_image="${SOCIALWEB_IT_GUEST_IMAGE_DIR:-/var/lib/socialweb-computer/guest-image}"
mkdir -p "$state"

say() { echo "dev-droplet: $*" >&2; }
ip_of() { cat "$state/${1:-dev-work-0001}.ip" 2>/dev/null || { say "no droplet ${1:-dev-work-0001}; run create"; exit 1; }; }
rssh() { local n="$1"; shift; ssh -i "$key_file" -o StrictHostKeyChecking=accept-new -o ServerAliveInterval=30 "root@$(ip_of "$n")" "$@"; }

cmd="${1:-}"
[ -n "$cmd" ] || usage
shift

case "$cmd" in
  create)
    name="${1:-dev-work-0001}"
    [ "$(doctl compute droplet list --tag-name dev-work --no-header --format ID | grep -c .)" -lt 2 ] || { say "two dev-work droplets exist already"; exit 1; }
    key_id="$(doctl compute ssh-key list --no-header --format ID,FingerPrint | awk -v fp="$(ssh-keygen -E md5 -lf "$key_file.pub" | sed 's/^[0-9]* MD5://; s/ .*//')" '$2 == fp { print $1 }')"
    [ -n "$key_id" ] || { say "key $key_file.pub is not registered with doctl"; exit 1; }
    for size in $sizes; do
      for region in $regions; do
        if out="$(doctl compute droplet create "$name" --size "$size" --image "$image" --region "$region" --tag-name dev-work --ssh-keys "$key_id" --wait --no-header --format ID 2>&1)"; then
          ip=""
          until [ -n "$ip" ]; do ip="$(doctl compute droplet get "$out" --no-header --format PublicIPv4)"; sleep 2; done
          echo "$ip" >"$state/$name.ip"
          say "$name created: $size in $region at $ip"
          break 2
        fi
        say "$size in $region: ${out##*: }"
      done
    done
    [ -f "$state/$name.ip" ] || exit 1
    until rssh "$name" true 2>/dev/null; do sleep 3; done
    ;;
  setup)
    name="${1:-dev-work-0001}"
    rssh "$name" bash -s <<'EOF'
set -euxo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq uidmap rsync zstd acl jq curl unzip dbus-user-session
modprobe tun
echo tun >/etc/modules-load.d/tun.conf
printf 'kernel.apparmor_restrict_unprivileged_userns=0\nuser.max_user_namespaces=1000000\n' >/etc/sysctl.d/60-swc.conf
sysctl --system >/dev/null
id test-0001 >/dev/null 2>&1 || useradd -m -s /bin/bash -G kvm test-0001
sed -i '/^test-0001:/d' /etc/subuid /etc/subgid
echo 'test-0001:655360:1048576' >>/etc/subuid
echo 'test-0001:655360:1048576' >>/etc/subgid
mkdir -p /etc/systemd/system/user@.service.d
printf '[Service]\nDelegate=cpu memory pids\n' >/etc/systemd/system/user@.service.d/swc-delegate.conf
systemctl daemon-reload
loginctl enable-linger test-0001
sleep 2
systemctl restart "user@$(id -u test-0001).service"
mkdir -p /var/lib/socialweb-computer /var/tmp/swcit /var/tmp/it-art
chmod 1777 /var/tmp/swcit
chown test-0001:test-0001 /var/tmp/it-art
test -c /dev/kvm
EOF
    ;;
  push)
    publish="${1:?publish dir}"
    name="${2:-dev-work-0001}"
    ssh_e="ssh -i $key_file -o StrictHostKeyChecking=accept-new"
    rsync -a --delete -e "$ssh_e" "$publish/" "root@$(ip_of "$name"):$publish/"
    rsync -a -z -e "$ssh_e" "$guest_image/" "root@$(ip_of "$name"):/var/lib/socialweb-computer/guest-image/"
    ;;
  swc)
    name="dev-work-0001"
    [ "${1:-}" = "--" ] || { name="$1"; shift; }
    shift
    publish="${SOCIALWEB_PUBLISH_DIR:?set SOCIALWEB_PUBLISH_DIR to the pushed publish dir}"
    uid="$(rssh "$name" id -u test-0001)"
    rssh "$name" runuser -u test-0001 -- env HOME=/home/test-0001 XDG_RUNTIME_DIR=/run/user/$uid DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$uid/bus PATH=/usr/local/bin:/usr/bin:/bin LANG=C.UTF-8 "$publish/bin/socialweb-computer" "$@"
    ;;
  it)
    pattern="${1:?test regex}"
    publish="${2:?publish dir}"
    name="${3:-dev-work-0001}"
    repo="$(cd "$(dirname "$0")/.." && pwd)"
    bin="$state/it.test"
    (cd "$repo" && go test -c -tags integration -o "$bin" ./test/integration/)
    scp -q -i "$key_file" -o StrictHostKeyChecking=accept-new "$bin" "root@$(ip_of "$name"):/var/tmp/it.test"
    uid="$(rssh "$name" id -u test-0001)"
    rssh "$name" runuser -u test-0001 -- env HOME=/home/test-0001 XDG_RUNTIME_DIR=/run/user/$uid DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/$uid/bus PATH=/usr/local/bin:/usr/bin:/bin LANG=C.UTF-8 \
      SOCIALWEB_IT_SKIP_BUILD=1 SOCIALWEB_IT_PUBLISH="$publish" SOCIALWEB_IT_LIVE_SLOT=none SOCIALWEB_IT_RUN_ROOT=/var/tmp/swcit SOCIALWEB_IT_ARTIFACTS=/var/tmp/it-art/run \
      /var/tmp/it.test -test.v -test.timeout 40m -test.run "$pattern"
    ;;
  ssh)
    name="${1:-dev-work-0001}"
    [ $# -eq 0 ] || shift
    rssh "$name" "$@"
    ;;
  list)
    doctl compute droplet list --tag-name dev-work --format ID,Name,PublicIPv4,Memory,VCPUs,Region,Status
    ;;
  down)
    doctl compute droplet delete --tag-name dev-work --force
    rm -f "$state"/*.ip
    doctl compute droplet list --tag-name dev-work --no-header --format ID | grep -c . | grep -qx 0 && say "no dev-work droplet is left"
    ;;
  *)
    usage
    ;;
esac
