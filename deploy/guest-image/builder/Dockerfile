FROM docker.io/library/debian@sha256:913f6706df59a68922d1dd08f78c2476560a8d367897200a6005b00e5f67c2d5

ARG SNAPSHOT=20261005T000000Z

RUN rm -f /etc/apt/sources.list.d/debian.sources \
 && printf 'deb [check-valid-until=no] http://snapshot.debian.org/archive/debian/%s trixie main\ndeb [check-valid-until=no] http://snapshot.debian.org/archive/debian-security/%s trixie-security main\n' "$SNAPSHOT" "$SNAPSHOT" > /etc/apt/sources.list \
 && apt-get -o Acquire::Retries=5 update \
 && DEBIAN_FRONTEND=noninteractive apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
      bc bison flex make gcc libc6-dev libelf-dev libssl-dev cpio kmod perl python3 \
      gcc-aarch64-linux-gnu libc6-dev-arm64-cross \
      e2fsprogs libarchive13t64 xz-utils zstd ca-certificates \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*
