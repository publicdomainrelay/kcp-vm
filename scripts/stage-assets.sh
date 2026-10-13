#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
versions="$root/internal/versions/versions.json"
blobs="$root/internal/assets/blobs"
cache=""
print_pins=0
platforms=()
only=()

usage() {
  echo "usage: $0 [--platform linux/amd64|linux/arm64]... [--asset NAME]... [--cache DIR] [--blobs DIR] [--print-pins]" >&2
  echo "  downloads every pinned asset of versions.json, verifies its sha256 (and the upstream signature" >&2
  echo "  where one is published), repacks it as internal/assets/blobs/<goos>_<goarch>/<asset>.tar.zst and" >&2
  echo "  checks the unpacked tree against its treeSha256 pin. --print-pins prints the measured pins and" >&2
  echo "  stages even when a tree pin is missing or differs, so versions.json can be updated. --cache keeps" >&2
  echo "  the downloads (keyed by sha256) in DIR instead of internal/assets/blobs/.cache. --blobs writes" >&2
  echo "  <goos>_<goarch>/<asset>.tar.zst under DIR instead of internal/assets/blobs (the --dev build's cache)." >&2
  exit 2
}

while [ $# -gt 0 ]; do
  case "$1" in
    --platform) platforms+=("$2"); shift 2 ;;
    --asset) only+=("$2"); shift 2 ;;
    --cache) cache="$2"; shift 2 ;;
    --blobs) blobs="$2"; shift 2 ;;
    --print-pins) print_pins=1; shift ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done

for tool in curl jq gpg ar tar zstd sha256sum unzip go xz; do
  command -v "$tool" >/dev/null 2>&1 || { echo "stage-assets: $tool is required on the build host" >&2; exit 2; }
done

if [ ${#platforms[@]} -eq 0 ]; then
  mapfile -t platforms < <(jq -r '.platforms | to_entries[] | select(.value.status == "release") | .key' "$versions")
fi

cache="${cache:-$blobs/.cache}"
mkdir -p "$cache" "$blobs/.work"
work="$(mktemp -d "$blobs/.work/run.XXXXXX")"
trap 'chmod -R u+w "$work" 2>/dev/null; rm -rf "$work"' EXIT
export GNUPGHOME="$work/gnupg"
mkdir -m 0700 "$GNUPGHOME"

die() { echo "stage-assets: $*" >&2; exit 1; }
say() { echo "stage-assets: $*" >&2; }

sha_of() { sha256sum "$1" | cut -d' ' -f1; }

fetch_pinned() {
  local url="$1" want="$2" out="$cache/$2"
  if [ -f "$out" ] && [ "$(sha_of "$out")" = "$want" ]; then
    echo "$out"
    return
  fi
  curl -fsSL --retry 3 -o "$out.part" "$url" || die "download failed: $url"
  local got
  got="$(sha_of "$out.part")"
  if [ "$got" != "$want" ]; then
    rm -f "$out.part"
    die "sha256 mismatch for $url: got $got, pinned $want"
  fi
  mv "$out.part" "$out"
  echo "$out"
}

fetch_fresh() {
  local url="$1" out="$2"
  curl -fsSL --retry 3 -o "$out" "$url" || die "download failed: $url"
}

key_ring() {
  local url="$1" fpr="$2" ring="$work/keys/$2.gpg"
  if [ ! -f "$ring" ]; then
    mkdir -p "$work/keys"
    fetch_fresh "$url" "$work/keys/$fpr.asc"
    gpg --batch --quiet --no-default-keyring --keyring "$ring" --import "$work/keys/$fpr.asc" 2>/dev/null
    gpg --batch --no-default-keyring --keyring "$ring" --with-colons --list-keys \
      | awk -F: '$1=="pub"{p=1;next} p&&$1=="fpr"{print $10; p=0}' | grep -qx "$fpr" \
      || die "key from $url does not carry primary fingerprint $fpr"
  fi
  echo "$ring"
}

gpg_verify() {
  local ring="$1" fpr="$2" sig="$3" data="${4:-}" out="${5:-}" status
  if [ -n "$data" ]; then
    status="$(gpg --batch --no-default-keyring --keyring "$ring" --trust-model always --status-fd 1 --verify "$sig" "$data" 2>/dev/null || true)"
  else
    status="$(gpg --batch --no-default-keyring --keyring "$ring" --trust-model always --status-fd 1 --output "$out" --decrypt "$sig" 2>/dev/null || true)"
  fi
  echo "$status" | awk '$2=="VALIDSIG"{print $NF}' | grep -qx "$fpr" || die "signature on $sig does not verify with $fpr"
}

check_checksums() {
  local f="$1" name="$2" want="$3"
  if awk -v n="$name" -v w="$want" '{s=$NF; sub(/^\*/,"",s); sub(/^.*\//,"",s)} $1==w && (s==n || NF==1){ok=1} END{exit !ok}' "$f"; then
    return
  fi
  die "checksum file for $name does not list the pinned $want"
}

copy_members() {
  local from="$1" to="$2" members="$3" src dst
  while IFS=$'\t' read -r src dst; do
    [ -n "$src" ] || continue
    if [ ! -e "$from/$src" ]; then
      (cd "$from" && find . | head -40) >&2
      die "member $src is not in the unpacked archive (listing above)"
    fi
    mkdir -p "$(dirname "$to/$dst")"
    cp -L "$from/$src" "$to/$dst"
    chmod 0755 "$to/$dst"
  done < <(echo "$members" | jq -r 'to_entries[] | "\(.key)\t\(.value)"')
}

stage_file() {
  local spec="$1" tree="$2" x="$3" url want archive file
  url="$(echo "$spec" | jq -r .url)"
  want="$(echo "$spec" | jq -r .sha256)"
  archive="$(echo "$spec" | jq -r .archive)"
  file="$(fetch_pinned "$url" "$want")"
  if [ "$(echo "$spec" | jq -r '.checksums // empty')" != "" ]; then
    local cs name sig
    cs="$x/checksums"
    fetch_fresh "$(echo "$spec" | jq -r .checksums.url)" "$cs"
    name="$(echo "$spec" | jq -r .checksums.name)"
    sig="$(echo "$spec" | jq -r '.checksums.signature // empty')"
    if [ -n "$sig" ]; then
      local fpr ring
      fpr="$(echo "$spec" | jq -r .checksums.fingerprint)"
      ring="$(key_ring "$(echo "$spec" | jq -r .checksums.key)" "$fpr")"
      fetch_fresh "$sig" "$x/checksums.sig"
      gpg_verify "$ring" "$fpr" "$x/checksums.sig" "$cs"
    fi
    check_checksums "$cs" "$name" "$want"
  fi
  mkdir -p "$x/u"
  case "$archive" in
    tar.gz) tar -xzf "$file" -C "$x/u" ;;
    zip) unzip -q "$file" -d "$x/u" ;;
    raw) cp "$file" "$x/u/$(echo "$spec" | jq -r '.members | keys[0]')" ;;
    *) die "unknown archive kind $archive" ;;
  esac
  copy_members "$x/u" "$tree" "$(echo "$spec" | jq -c .members)"
}

stage_oci() {
  local spec="$1" tree="$2" x="$3" plat="$4" reg repo index manifest layer tok f
  reg="$(echo "$spec" | jq -r .registry)"
  repo="$(echo "$spec" | jq -r .repository)"
  index="$(echo "$spec" | jq -r .index)"
  manifest="$(echo "$spec" | jq -r .manifest)"
  layer="$(echo "$spec" | jq -r .layer)"
  tok="$(curl -fsSL "https://$reg/token?scope=repository:$repo:pull" | jq -r .token)"
  local accept="application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"
  curl -fsSL -H "Authorization: Bearer $tok" -H "Accept: $accept" -o "$x/index.json" "https://$reg/v2/$repo/manifests/$index"
  [ "sha256:$(sha_of "$x/index.json")" = "$index" ] || die "$repo index does not hash to $index"
  jq -e --arg d "$manifest" --arg os "${plat%/*}" --arg a "${plat#*/}" \
    '.manifests[] | select(.digest == $d and .platform.os == $os and .platform.architecture == $a)' "$x/index.json" >/dev/null \
    || die "$repo index $index does not name $manifest for $plat"
  curl -fsSL -H "Authorization: Bearer $tok" -H "Accept: $accept" -o "$x/manifest.json" "https://$reg/v2/$repo/manifests/$manifest"
  [ "sha256:$(sha_of "$x/manifest.json")" = "$manifest" ] || die "$repo manifest does not hash to $manifest"
  jq -e --arg d "$layer" '.layers[] | select(.digest == $d)' "$x/manifest.json" >/dev/null || die "$manifest does not name layer $layer"
  f="$cache/${layer#sha256:}"
  if [ ! -f "$f" ] || [ "sha256:$(sha_of "$f")" != "$layer" ]; then
    curl -fsSL -H "Authorization: Bearer $tok" -o "$f.part" "https://$reg/v2/$repo/blobs/$layer"
    [ "sha256:$(sha_of "$f.part")" = "$layer" ] || { rm -f "$f.part"; die "layer does not hash to $layer"; }
    mv "$f.part" "$f"
  fi
  mkdir -p "$x/u"
  tar -xzf "$f" -C "$x/u"
  copy_members "$x/u" "$tree" "$(echo "$spec" | jq -c .members)"
}

stage_script() {
  local spec="$1" tree="$2" x="$3" script want out
  script="$root/$(echo "$spec" | jq -r .script)"
  want="$(echo "$spec" | jq -r .sha256)"
  mkdir -p "$x/u"
  "$script" --out "$x/u" >&2
  out="$x/u/$(echo "$spec" | jq -r '.members | keys[0]')"
  [ "$(sha_of "$out")" = "$want" ] || die "$script built $(sha_of "$out"), pinned $want"
  copy_members "$x/u" "$tree" "$(echo "$spec" | jq -c .members)"
}

stage_go() {
  local spec="$1" tree="$2" x="$3" plat="$4" pkg module want out
  pkg="$(echo "$spec" | jq -r .package)"
  module="$(echo "$spec" | jq -r '.module // "."')"
  want="$(echo "$spec" | jq -r '.sha256 // ""')"
  out="$x/u/$(echo "$spec" | jq -r '.members | keys[0]')"
  mkdir -p "$x/u"
  (cd "$root/$module" && CGO_ENABLED=0 GOOS="${plat%/*}" GOARCH="${plat#*/}" go build -trimpath -buildvcs=false \
    -ldflags='-s -w -buildid=' -o "$out" "$pkg")
  say "$module $pkg $plat binary sha256 $(sha_of "$out")"
  if [ -n "$want" ] && [ "$(sha_of "$out")" != "$want" ]; then
    echo "stage-assets: $module $pkg $plat built $(sha_of "$out"), pinned $want" >&2
    [ "$print_pins" = 1 ] || die "$module $pkg $plat: sha256 differs from the pin"
  fi
  copy_members "$x/u" "$tree" "$(echo "$spec" | jq -c .members)"
}

deb_arch() { case "$1" in linux/amd64) echo amd64 ;; linux/arm64) echo arm64 ;; *) die "no deb arch for $1" ;; esac; }

repo_index() {
  local asset="$1" repo="$2" plat="$3" x="$4" arch base comp fpr ring suite idx
  arch="$(deb_arch "$plat")"
  base="$(jq -r --arg a "$asset" --arg r "$repo" --arg p "$plat" '.assets[$a].repos[$r].base[$p]' "$versions")"
  comp="$(jq -r --arg a "$asset" --arg r "$repo" '.assets[$a].repos[$r].component' "$versions")"
  fpr="$(jq -r --arg a "$asset" --arg r "$repo" '.assets[$a].repos[$r].fingerprint' "$versions")"
  ring="$(key_ring "$(jq -r --arg a "$asset" --arg r "$repo" '.assets[$a].repos[$r].key' "$versions")" "$fpr")"
  : >"$x/$repo.packages"
  for suite in $(jq -r --arg a "$asset" --arg r "$repo" '.assets[$a].repos[$r].suites[]' "$versions"); do
    fetch_fresh "${base}dists/$suite/InRelease" "$x/$suite.InRelease"
    gpg_verify "$ring" "$fpr" "$x/$suite.InRelease" "" "$x/$suite.Release"
    local found=""
    for ext in xz gz; do
      idx="$comp/binary-$arch/Packages.$ext"
      local sum
      sum="$(awk -v f="$idx" '/^SHA256:/{s=1;next} /^[^ ]/{s=0} s && $3==f {print $1}' "$x/$suite.Release" | head -1)"
      [ -n "$sum" ] || continue
      local pf
      pf="$(fetch_pinned "${base}dists/$suite/$idx" "$sum")"
      case "$ext" in xz) xz -dc "$pf" >>"$x/$repo.packages" ;; gz) gzip -dc "$pf" >>"$x/$repo.packages" ;; esac
      echo >>"$x/$repo.packages"
      found=1
      break
    done
    [ -n "$found" ] || die "$suite InRelease names no $comp/binary-$arch/Packages index"
  done
  echo "$base"
}

stage_debs() {
  local asset="$1" spec="$2" tree="$3" x="$4" plat="$5" arch repo base n i pkg ver pool want fname deb
  arch="$(deb_arch "$plat")"
  mkdir -p "$x/root"
  declare -A bases=()
  for repo in $(jq -r --arg a "$asset" '.assets[$a].repos | keys[]' "$versions"); do
    bases[$repo]="$(repo_index "$asset" "$repo" "$plat" "$x")"
  done
  n="$(jq -r --arg a "$asset" '.assets[$a].debs | length' "$versions")"
  for ((i = 0; i < n; i++)); do
    read -r pkg ver repo pool want < <(jq -r --arg a "$asset" --arg p "$plat" --argjson i "$i" \
      '.assets[$a].debs[$i] | "\(.package) \(.version) \(.repo) \(.pool) \(.sha256[$p])"' "$versions")
    fname="$(awk -v RS= -v p="$pkg" -v v="$ver" '
      { pk=""; vv=""; fn=""; sh="";
        n=split($0, L, "\n");
        for (j=1;j<=n;j++) { split(L[j], kv, ": ");
          if (kv[1]=="Package") pk=kv[2]; else if (kv[1]=="Version") vv=kv[2];
          else if (kv[1]=="Filename") fn=kv[2]; else if (kv[1]=="SHA256") sh=kv[2] }
        if (pk==p && vv==v) { print fn " " sh; exit } }' "$x/$repo.packages")"
    if [ -n "$fname" ]; then
      [ "${fname#* }" = "$want" ] || die "$pkg $ver: the signed $repo index names sha256 ${fname#* }, pinned $want"
      deb="$(fetch_pinned "${bases[$repo]}${fname% *}" "$want")"
    else
      say "warn: $pkg $ver is no longer in the signed $repo index for $arch (superseded); verified by its pinned sha256 alone"
      local nover="${ver#*:}"
      deb="$(fetch_pinned "${bases[$repo]}$pool${pkg}_${nover}_$arch.deb" "$want" 2>/dev/null)" \
        || deb="$(fetch_pinned "${bases[$repo]}$pool${pkg}_${nover}_all.deb" "$want")"
    fi
    rm -rf "$x/deb" && mkdir -p "$x/deb"
    (cd "$x/deb" && ar x "$deb")
    tar -xf "$x"/deb/data.tar.* -C "$x/root"
  done
  case "$(echo "$spec" | jq -r .layout)" in
    members) copy_members "$x/root" "$tree" "$(echo "$spec" | jq -c .members)" ;;
    pgdg-tree)
      local triplet
      case "$arch" in amd64) triplet=x86_64-linux-gnu ;; arm64) triplet=aarch64-linux-gnu ;; esac
      mkdir -p "$tree/usr/lib/postgresql/18" "$tree/usr/share/postgresql" "$tree/usr/lib/sys"
      cp -a "$x/root/usr/lib/postgresql/18/bin" "$x/root/usr/lib/postgresql/18/lib" "$tree/usr/lib/postgresql/18/"
      cp -a "$x/root/usr/share/postgresql/18" "$tree/usr/share/postgresql/"
      rm -rf "$tree/usr/share/postgresql/18/man" "$tree/usr/lib/postgresql/18/lib/bitcode"
      for d in "$x/root/lib/$triplet" "$x/root/usr/lib/$triplet"; do
        [ -d "$d" ] || continue
        find "$d" -maxdepth 1 -name '*.so*' \( -type f -o -type l \) -exec cp -a {} "$tree/usr/lib/sys/" \;
      done
      find "$tree" -type l | while read -r l; do
        case "$(readlink "$l")" in /*) die "absolute symlink $l -> $(readlink "$l") in the postgres tree" ;; esac
      done
      ;;
    *) die "unknown deb layout" ;;
  esac
}

(cd "$root" && go build -o "$work/assetpack" ./internal/assets/assetpack)

failed=0
for plat in "${platforms[@]}"; do
  pdir="$blobs/${plat/\//_}"
  mkdir -p "$pdir"
  for asset in $(jq -r --arg p "$plat" '.platforms[$p].assets[]' "$versions"); do
    if [ ${#only[@]} -gt 0 ] && [[ ! " ${only[*]} " == *" $asset "* ]]; then
      continue
    fi
    spec="$(jq -c --arg a "$asset" --arg p "$plat" '.assets[$a].fetch[$p]' "$versions")"
    [ "$spec" != "null" ] || die "$asset has no fetch spec for $plat"
    x="$work/$asset-${plat/\//_}"
    tree="$x/tree"
    mkdir -p "$tree"
    say "$plat $asset"
    case "$(echo "$spec" | jq -r .kind)" in
      file) stage_file "$spec" "$tree" "$x" ;;
      oci) stage_oci "$spec" "$tree" "$x" "$plat" ;;
      debs) stage_debs "$asset" "$spec" "$tree" "$x" "$plat" ;;
      script) stage_script "$spec" "$tree" "$x" ;;
      go) stage_go "$spec" "$tree" "$x" "$plat" ;;
      *) die "$asset: unknown fetch kind" ;;
    esac
    digest="$("$work/assetpack" digest "$tree")"
    raw="$(find "$tree" -type f -printf '%s\n' | awk '{s+=$1} END{print s+0}')"
    "$work/assetpack" tar "$tree" | zstd -q -19 --long=27 -T0 -o "$x/blob"
    zbytes="$(stat -c %s "$x/blob")"
    pin="$(jq -r --arg a "$asset" --arg p "$plat" '.assets[$a].platforms[$p].treeSha256 // ""' "$versions")"
    if [ "$print_pins" = 1 ]; then
      jq -cn --arg a "$asset" --arg p "$plat" --arg d "$digest" --argjson r "$raw" --argjson z "$zbytes" \
        '{asset:$a, platform:$p, treeSha256:$d, rawBytes:$r, zstdBytes:$z}'
    fi
    if [ "$pin" != "$digest" ]; then
      echo "stage-assets: $asset $plat unpacks to tree $digest, versions.json pins '${pin}'" >&2
      if [ "$print_pins" != 1 ]; then
        failed=1
        continue
      fi
    fi
    mv "$x/blob" "$pdir/$asset.tar.zst"
    chmod -R u+w "$x" && rm -rf "$x"
    say "ok $plat $asset tree $digest raw $raw zstd $zbytes"
  done
done
exit "$failed"
