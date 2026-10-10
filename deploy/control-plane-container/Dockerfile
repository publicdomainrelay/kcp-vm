# syntax=docker/dockerfile:1.7

ARG FEDORA=registry.fedoraproject.org/fedora:44@sha256:2cdfedd312a3c92e0d9932ee18d9d2dd80b00fa0bb4ebdaa86736195b5eaa974

FROM ${FEDORA} AS gobuild

WORKDIR /src

COPY . /src

ENV PATH=/go/toolchain/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    GOPATH=/go \
    GOMODCACHE=/go/pkg/mod \
    GOCACHE=/go/cache \
    GOTOOLCHAIN=local \
    GOPROXY=off \
    GOFLAGS=-mod=mod \
    CGO_ENABLED=0

RUN --mount=type=bind,from=goroot,target=/go/toolchain,ro \
    --mount=type=bind,from=gomod,target=/host-mod,ro \
    --mount=type=cache,id=gomod,target=/go/pkg/mod \
    --mount=type=cache,id=gocache,target=/go/cache \
    sh -ec 'if [ ! -d /go/pkg/mod/k8s.io ]; then cp -a /host-mod/. /go/pkg/mod/; fi; go build -o /usr/local/bin/socialweb-computer-kcp-check-host ./cmd/socialweb-computer-kcp-check-host && go build -o /usr/local/bin/socialweb-nodeimage ./cmd/socialweb-nodeimage && go build -o /usr/local/bin/socialweb-gateway ./cmd/socialweb-gateway && go build -o /usr/local/bin/socialweb-credential ./cmd/socialweb-credential && go build -o /usr/local/bin/socialweb-clusterprovider ./cmd/socialweb-clusterprovider && go build -o /usr/local/bin/socialweb-cloud-controller-manager ./cmd/socialweb-cloud-controller-manager && go build -o /usr/local/bin/socialweb-vault ./cmd/socialweb-vault && go build -ldflags "-s -w" -buildvcs=false -o /usr/local/bin/kcpagent ./cmd/socialweb-nodeagent'

FROM gobuild AS credentials

RUN --mount=type=bind,from=goroot,target=/go/toolchain,ro \
    --mount=type=bind,from=gomod,target=/host-mod,ro \
    --mount=type=cache,id=gomod,target=/go/pkg/mod \
    --mount=type=cache,id=gocache,target=/go/cache \
    sh -ec 'mkdir -p /downloads; for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do os=${target%/*}; arch=${target#*/}; GOOS=$os GOARCH=$arch go build -ldflags "-s -w" -buildvcs=false -o /downloads/socialweb-credential-$os-$arch ./cmd/socialweb-credential; done; ls -la /downloads'

FROM ${FEDORA} AS tools

RUN dnf install -y --setopt=install_weak_deps=False \
      squashfs-tools cpio xz zstd e2fsprogs busybox coreutils findutils tar gzip \
 && dnf clean all

FROM tools AS nodeimage

ARG NODEIMAGE_CONFIG=/home/johnandersen777/kcp-image/nodeimage.json

ARG KCP_IMAGE=/home/johnandersen777/kcp-image
ARG REPO=/home/johnandersen777/src/publicdomainrelay-kcp/socialweb-computer-kcp

ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

COPY --from=binaries /k3s /usr/local/bin/k3s

COPY --from=gobuild /usr/local/bin/socialweb-computer-kcp-check-host /usr/local/bin/socialweb-computer-kcp-check-host

COPY --from=gobuild /usr/local/bin/socialweb-nodeimage /usr/local/bin/socialweb-nodeimage

COPY go.mod go.sum ${REPO}/

COPY cmd/socialweb-nodeagent ${REPO}/cmd/socialweb-nodeagent

COPY api ${REPO}/api

COPY internal ${REPO}/internal

RUN mkdir -p ${KCP_IMAGE}/out ${KCP_IMAGE}/stage ${KCP_IMAGE}/cache

COPY --from=kcpimage nodeimage.json ${NODEIMAGE_CONFIG}

WORKDIR ${REPO}

RUN --mount=type=bind,from=goroot,target=/go/toolchain,ro \
    --mount=type=bind,from=gomod,target=/host-mod,ro \
    --mount=type=cache,id=gomod,target=/go/pkg/mod \
    --mount=type=cache,id=gocache,target=/go/cache \
    --mount=type=bind,from=artifacts,target=${KCP_IMAGE}/artifacts,ro \
    --mount=type=bind,from=bootdir,target=/boot,ro \
    --mount=type=bind,from=modulesdir,target=/lib/modules,ro \
    sh -ec 'set -e; if [ ! -d /go/pkg/mod/k8s.io ]; then cp -a /host-mod/. /go/pkg/mod/; fi; export PATH=/go/toolchain/bin:$PATH GOPATH=/go GOMODCACHE=/go/pkg/mod GOCACHE=/go/cache GOTOOLCHAIN=local GOFLAGS=-mod=mod; socialweb-computer-kcp-check-host -config "$0" -build; socialweb-nodeimage -config "$0"; mkdir -p /dist; for d in '"${KCP_IMAGE}"'/out/sha256-*; do cp -a "$d" /dist/; done; ls -la /dist' "$NODEIMAGE_CONFIG"

FROM tools AS nodeimage-qemu

ARG NODEIMAGE_CONFIG=/home/johnandersen777/kcp-image/nodeimage-qemu.json

ARG KCP_IMAGE=/home/johnandersen777/kcp-image
ARG REPO=/home/johnandersen777/src/publicdomainrelay-kcp/socialweb-computer-kcp

ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

COPY --from=binaries /k3s /usr/local/bin/k3s

COPY --from=gobuild /usr/local/bin/socialweb-computer-kcp-check-host /usr/local/bin/socialweb-computer-kcp-check-host

COPY --from=gobuild /usr/local/bin/socialweb-nodeimage /usr/local/bin/socialweb-nodeimage

COPY . ${REPO}

RUN mkdir -p ${KCP_IMAGE}/out ${KCP_IMAGE}/stage ${KCP_IMAGE}/stage-qemu ${KCP_IMAGE}/cache

COPY --from=kcpimage nodeimage-qemu.json ${NODEIMAGE_CONFIG}

WORKDIR ${REPO}

RUN --mount=type=bind,from=goroot,target=/go/toolchain,ro \
    --mount=type=bind,from=gomod,target=/host-mod,ro \
    --mount=type=cache,id=gomod,target=/go/pkg/mod \
    --mount=type=cache,id=gocache,target=/go/cache \
    --mount=type=bind,from=artifacts,target=${KCP_IMAGE}/artifacts,ro \
    --mount=type=bind,from=bootdir,target=/boot,ro \
    --mount=type=bind,from=modulesdir,target=/lib/modules,ro \
    sh -ec 'set -e; if [ ! -d /go/pkg/mod/k8s.io ]; then cp -a /host-mod/. /go/pkg/mod/; fi; export PATH=/go/toolchain/bin:$PATH GOPATH=/go GOMODCACHE=/go/pkg/mod GOCACHE=/go/cache GOTOOLCHAIN=local GOFLAGS=-mod=mod; socialweb-computer-kcp-check-host -config "$0" -build; socialweb-nodeimage -config "$0"; mkdir -p /dist; for d in '"${KCP_IMAGE}"'/out/sha256-*; do cp -a "$d" /dist/; done; ls -la /dist' "$NODEIMAGE_CONFIG"

FROM scratch AS extract

COPY --from=nodeimage /dist/ /

COPY --from=nodeimage-qemu /dist/ /

FROM ${FEDORA} AS controlplane

RUN dnf install -y --setopt=install_weak_deps=False \
      postgresql-server postgresql util-linux coreutils findutils procps-ng tar gzip hostname \
 && dnf clean all \
 && useradd -u 1000 -m -s /bin/bash kcp \
 && install -d -o kcp -g kcp -m 0755 /etc/rancher/node \
 && install -o kcp -g kcp -m 0600 /dev/null /etc/rancher/node/password \
 && head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n' > /etc/rancher/node/password \
 && chown kcp:kcp /etc/rancher/node/password \
 && chmod 0600 /etc/rancher/node/password

COPY --from=gobuild /usr/local/bin/socialweb-computer-kcp-check-host /usr/local/bin/socialweb-computer-kcp-check-host

COPY --from=binaries /k3s /usr/local/bin/k3s

COPY --from=binaries /kine /usr/local/bin/kine

COPY --from=binaries /kcp /usr/local/bin/kcp

COPY --from=binaries /kubectl /usr/local/bin/kubectl

COPY --from=binaries /bao /usr/local/bin/bao

ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    HOME=/home/kcp

WORKDIR /src

USER 1000:1000

CMD ["socialweb-computer-kcp-check-host"]

FROM controlplane AS k8s

ARG DENO_VERSION=2.9.3

USER root

RUN dnf install -y --setopt=install_weak_deps=False yq iproute socat openssl unzip \
 && dnf clean all \
 && curl -fsSL "https://github.com/denoland/deno/releases/download/v${DENO_VERSION}/deno-x86_64-unknown-linux-gnu.zip" -o /tmp/deno.zip \
 && unzip -o /tmp/deno.zip -d /usr/local/bin \
 && rm /tmp/deno.zip \
 && chmod 0755 /usr/local/bin/deno

COPY --from=gobuild /usr/local/bin/socialweb-gateway /usr/local/bin/socialweb-gateway

COPY --from=gobuild /usr/local/bin/socialweb-credential /usr/local/bin/socialweb-credential

COPY --from=gobuild /usr/local/bin/socialweb-clusterprovider /usr/local/bin/socialweb-clusterprovider

COPY --from=gobuild /usr/local/bin/socialweb-cloud-controller-manager /usr/local/bin/socialweb-cloud-controller-manager

COPY --from=gobuild /usr/local/bin/socialweb-vault /usr/local/bin/socialweb-vault

COPY --from=k8stree / /opt/org-root/

COPY --from=credentials /downloads/ /opt/org-root/socialweb-computer-kcp/socialweb-computer-k8s/web/downloads/

COPY deploy/ /opt/socialweb/deploy/

COPY deploy/container-entrypoint.sh /usr/local/bin/container-entrypoint

ENV DENO_DIR=/opt/deno-dir \
    SOCIALWEB_CONTAINER_DOOR_DIR=/opt/org-root/socialweb-computer-kcp/socialweb-computer-k8s

RUN install -d -o kcp -g kcp -m 0755 /opt/deno-dir \
 && chown -R kcp:kcp /opt/org-root \
 && install -d -o kcp -g kcp -m 0700 /var/lib/socialweb /var/lib/socialweb/kcp /var/lib/socialweb/mint /var/lib/socialweb/clusterprovider /var/lib/socialweb/door \
 && chmod 0755 /usr/local/bin/container-entrypoint /usr/local/bin/socialweb-gateway /usr/local/bin/socialweb-credential /usr/local/bin/socialweb-clusterprovider /usr/local/bin/socialweb-cloud-controller-manager /usr/local/bin/socialweb-vault

USER 1000:1000

RUN --mount=type=cache,id=deno,target=/opt/deno-dir,uid=1000,gid=1000 \
 cd "$SOCIALWEB_CONTAINER_DOOR_DIR" \
 && deno install --entrypoint hono-socialweb-computer-k8s/mod.ts \
 && deno cache hono-socialweb-computer-k8s/undici-preload.ts hono-socialweb-computer-k8s/mod.ts \
 && ls web/downloads

ENTRYPOINT ["/usr/local/bin/container-entrypoint"]

CMD []

FROM controlplane AS computer

USER root

RUN dnf install -y --setopt=install_weak_deps=False passt yq e2fsprogs iproute socat openssl qemu-system-x86-core \
 && ln -sf /usr/bin/qemu-system-x86_64 /usr/local/bin/qemu-system-x86_64 \
 && dnf clean all

COPY --from=gobuild /usr/local/bin/socialweb-nodeimage /usr/local/bin/socialweb-nodeimage

COPY --from=gobuild /usr/local/bin/socialweb-gateway /usr/local/bin/socialweb-gateway

COPY --from=gobuild /usr/local/bin/socialweb-credential /usr/local/bin/socialweb-credential

COPY --from=gobuild /usr/local/bin/socialweb-clusterprovider /usr/local/bin/socialweb-clusterprovider

COPY --from=gobuild /usr/local/bin/socialweb-cloud-controller-manager /usr/local/bin/socialweb-cloud-controller-manager

COPY --from=gobuild /usr/local/bin/socialweb-vault /usr/local/bin/socialweb-vault

COPY --from=gobuild /usr/local/bin/kcpagent /usr/local/bin/kcpagent

COPY --from=artifacts /bin/firecracker /usr/local/bin/firecracker

COPY --from=bootdir / /boot/

COPY --from=modulesdir / /usr/lib/modules/

COPY --from=artifacts /img/ubuntu-24.04.squashfs /home/johnandersen777/kcp-image/artifacts/img/ubuntu-24.04.squashfs

COPY --from=nodeimage /home/johnandersen777/kcp-image/kcpagent /home/johnandersen777/kcp-image/kcpagent

COPY --from=extract / /var/lib/socialweb/images

COPY deploy/container-nodeimage.json /home/johnandersen777/kcp-image/nodeimage.json

COPY deploy/container-nodeimage-qemu.json /home/johnandersen777/kcp-image/nodeimage-qemu.json

COPY deploy/ /opt/socialweb/deploy/

COPY deploy/container-entrypoint.sh /usr/local/bin/container-entrypoint

RUN install -d -o kcp -g kcp -m 0700 /var/lib/socialweb/kcp /var/lib/socialweb/mint /var/lib/socialweb/stage /var/lib/socialweb/clusterprovider \
 && chown -R kcp:kcp /var/lib/socialweb \
 && chmod 0755 /usr/local/bin/container-entrypoint /usr/local/bin/firecracker /usr/local/bin/kcpagent /usr/local/bin/socialweb-nodeimage /usr/local/bin/socialweb-gateway /usr/local/bin/socialweb-credential /usr/local/bin/socialweb-clusterprovider /usr/local/bin/socialweb-cloud-controller-manager /usr/local/bin/socialweb-vault

USER 1000:1000

ENTRYPOINT ["/usr/local/bin/container-entrypoint"]

CMD []
