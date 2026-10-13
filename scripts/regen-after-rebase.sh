#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

usage() {
  cat >&2 <<EOF
usage: $0

Run it while a rebase (or merge, or cherry-pick) is stopped on conflicts. For every conflicted path
that a generator writes, it takes one side (ours: during a rebase that is the branch being rebased
onto), reruns the generators and stages the result:

  go generate ./api/...    deploy/*-apiresourceschema.yaml, deploy/*-apiexport.yaml,
                           deploy/apiresourceschema-revisions.txt, the TypeScript wire types and
                           fixtures (cmd/socialweb-apigen -list names them all)
  deno install             deno.lock, at the root with --node-modules-dir=none

It never hand-merges generated output. The generators read api/v1alpha1, internal/apigen and
cmd/socialweb-apigen, so a conflict there must be resolved first (the script says so and stops).
It then prints the conflicts that remain (hand-written code, docs, plans, the zz_generated deepcopy
files, which no generator writes) and, when none remain, the command to continue.
EOF
  exit 2
}
[ $# -eq 0 ] || usage

say() { echo "regen-after-rebase: $*" >&2; }

op=""
gitdir="$(git rev-parse --git-dir)"
if [ -d "$gitdir/rebase-merge" ] || [ -d "$gitdir/rebase-apply" ]; then
  op=rebase
elif [ -f "$gitdir/MERGE_HEAD" ]; then
  op=merge
elif [ -f "$gitdir/CHERRY_PICK_HEAD" ]; then
  op=cherry-pick
fi

mapfile -t conflicted < <(git diff --name-only --diff-filter=U)
if [ ${#conflicted[@]} -eq 0 ]; then
  say "no conflicted paths${op:+ (a $op is in progress)}; regenerating anyway"
fi

inputs=()
for p in "${conflicted[@]}"; do
  case "$p" in
    api/*.go|api/*/*.go|internal/apigen/*|cmd/socialweb-apigen/*) inputs+=("$p") ;;
  esac
done
if [ ${#inputs[@]} -gt 0 ]; then
  say "the generators read these conflicted paths; resolve them by hand, git add them, then run this again:"
  printf '  %s\n' "${inputs[@]}"
  exit 1
fi

mapfile -t before < <(go run ./cmd/socialweb-apigen -root . -list | tr ' ' '\n' | grep .)
generated=()
lock_conflict=0
for p in "${conflicted[@]}"; do
  if [ "$p" = deno.lock ]; then
    lock_conflict=1
    generated+=("$p")
  elif printf '%s\n' "${before[@]}" | grep -qxF -- "$p"; then
    generated+=("$p")
  fi
done

for p in "${generated[@]}"; do
  if git cat-file -e ":2:$p" 2>/dev/null; then
    git checkout --ours -- "$p"
  elif git cat-file -e ":3:$p" 2>/dev/null; then
    git checkout --theirs -- "$p"
  else
    git rm -q --cached -- "$p"
    rm -f -- "$p"
  fi
  say "took ours of $p"
done

lockf=deploy/apiresourceschema-revisions.txt
if [ -f "$lockf" ] && [ -n "$(awk '{print $1}' "$lockf" | sort | uniq -d)" ]; then
  tmp="$(mktemp -d)"
  git show "HEAD:$lockf" >"$tmp/head" 2>/dev/null || : >"$tmp/head"
  awk 'NR == FNR { head[$1] = $2; next }
    $1 != "" { n[$1]++; h[$1, n[$1]] = $2; if (!($1 in seen)) { seen[$1] = 1; names[++k] = $1 } }
    END { for (i = 1; i <= k; i++) { nm = names[i]; pick = h[nm, 1]; for (j = 1; j <= n[nm]; j++) if (h[nm, j] == head[nm]) pick = h[nm, j]; print nm, pick } }' \
    "$tmp/head" "$lockf" | sort >"$tmp/lock"
  mv "$tmp/lock" "$lockf"
  rm -rf "$tmp"
  say "$lockf named a revision twice (both sides changed one schema under one revision); kept the HEAD hash. If go generate now asks for a Revision bump, bump it in api/v1alpha1/resources.go and run this again"
fi

say "go generate ./api/..."
go generate ./api/...
mapfile -t outputs < <(go run ./cmd/socialweb-apigen -root . -list | tr ' ' '\n' | grep .)
for p in "${generated[@]}"; do
  [ "$p" = deno.lock ] && continue
  if ! printf '%s\n' "${outputs[@]}" | grep -qxF -- "$p"; then
    say "warn: $p was taken from one side but apigen no longer writes it; it stays as taken"
  fi
done

if [ "$lock_conflict" = 1 ]; then
  deno="$(command -v deno || echo "$HOME/.deno/bin/deno")"
  say "$deno install --node-modules-dir=none (refreshes deno.lock for both sides' imports)"
  "$deno" install --node-modules-dir=none >&2
fi

stage=()
for p in "${outputs[@]}"; do
  [ -e "$p" ] && stage+=("$p")
done
[ "$lock_conflict" = 0 ] || stage+=(deno.lock)
git add -- "${stage[@]}"
say "regenerated and staged ${#stage[@]} generated paths (${#generated[@]} were conflicted)"

mapfile -t left < <(git diff --name-only --diff-filter=U)
if [ ${#left[@]} -gt 0 ]; then
  say "conflicts that remain (hand-resolve, git add):"
  printf '  %s\n' "${left[@]}"
  exit 1
fi
case "$op" in
  rebase) say "no conflicts remain: git rebase --continue" ;;
  merge) say "no conflicts remain: git commit" ;;
  cherry-pick) say "no conflicts remain: git cherry-pick --continue" ;;
  *) say "no conflicts remain" ;;
esac
