run_base="/run/user/$(id -u)"
if [ -d "$run_base" ] && [ -w "$run_base" ]; then
  run_dir="$run_base/swc-ci-podman"
else
  run_dir="$CI_STATE/podman/run-$(cat /proc/sys/kernel/random/boot_id)"
fi
mkdir -p "$CI_STATE/podman" "$run_dir" && chmod 0700 "$run_dir"
export XDG_RUNTIME_DIR="$run_dir"
export CONTAINERS_STORAGE_CONF="$CI_STATE/podman/storage.conf" CONTAINERS_CONF="$CI_STATE/podman/containers.conf"
printf '[storage]\ndriver = "overlay"\ngraphroot = "%s"\nrunroot = "%s"\n[storage.options.overlay]\nignore_chown_errors = "true"\n' "$CI_STATE/podman/storage" "$run_dir/storage" >"$CONTAINERS_STORAGE_CONF"
printf '[engine]\ncgroup_manager = "cgroupfs"\nevents_logger = "file"\n[containers]\ncgroups = "disabled"\n' >"$CONTAINERS_CONF"
if ! out="$(podman version --format 'podman {{.Server.Version}}' 2>&1)"; then
  case "$out" in
    *"unhandled reboot"*)
      echo "$out" >&2
      stale="$(printf '%s\n' "$out" | grep -o '"[^"]*"' | tr -d '"')"
      while IFS= read -r dir; do
        case "$dir" in
          "$run_dir"/*) rm -rf -- "$dir" ;;
          "$CI_STATE"/podman/run*/*) rm -rf -- "$CI_STATE"/podman/run "$CI_STATE"/podman/run-* "$CI_STATE"/podman/storage/db.sql* ;;
        esac
      done <<<"$stale"
      mkdir -p "$run_dir" && chmod 0700 "$run_dir"
      out="$(podman version --format 'podman {{.Server.Version}}')"
      ;;
    *) echo "$out" >&2; exit 1 ;;
  esac
fi
echo "$out"
