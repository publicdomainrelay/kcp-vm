# syntax=docker/dockerfile:1.7

ARG FEDORA=registry.fedoraproject.org/fedora:44@sha256:2cdfedd312a3c92e0d9932ee18d9d2dd80b00fa0bb4ebdaa86736195b5eaa974

# The External Secrets controller and its webhook run beside each cluster's control plane, in
# its process group (NEXT.yaml ESO-4). The binary is the pinned upstream image's own; that image
# is distroless with /bin -> usr/bin, so the real path is copied. The bundle is the same
# release's external-secrets.yaml, checked against its sha256; the provider installs its CRDs,
# ClusterRoles and webhook registration into each tenant and retires everything else in it.
# internal/versions/versions.json carries the same three values and a test holds them equal.
ARG EXTERNAL_SECRETS_VERSION=v2.11.0
ARG EXTERNAL_SECRETS_IMAGE=ghcr.io/external-secrets/external-secrets:v2.11.0@sha256:66fb710878cbf3eba4a927e35c5d75e29a202c9bab238aad64bfb23b786b962b
ARG EXTERNAL_SECRETS_BUNDLE_SHA256=c427124642886d240af8f28d294714a168d920ef8e5d55db14600e00ed0cfb80

FROM ${EXTERNAL_SECRETS_IMAGE} AS externalsecrets

FROM ${FEDORA} AS esobundle

ARG EXTERNAL_SECRETS_VERSION
ARG EXTERNAL_SECRETS_BUNDLE_SHA256

RUN mkdir -p /eso \
 && curl -fsSL "https://github.com/external-secrets/external-secrets/releases/download/${EXTERNAL_SECRETS_VERSION}/external-secrets.yaml" -o /eso/external-secrets.yaml \
 && echo "${EXTERNAL_SECRETS_BUNDLE_SHA256}  /eso/external-secrets.yaml" | sha256sum -c -

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
    sh -ec 'if [ ! -d /go/pkg/mod/k8s.io ]; then cp -a /host-mod/. /go/pkg/mod/; fi; go build -o /usr/local/bin/socialweb-computer-kcp-check-host ./cmd/socialweb-computer-kcp-check-host && go build -o /usr/local/bin/socialweb-gateway ./cmd/socialweb-gateway && go build -o /usr/local/bin/socialweb-credential ./cmd/socialweb-credential && go build -o /usr/local/bin/socialweb-clusterprovider ./cmd/socialweb-clusterprovider && go build -o /usr/local/bin/socialweb-cloud-controller-manager ./cmd/socialweb-cloud-controller-manager && go build -o /usr/local/bin/socialweb-vault ./cmd/socialweb-vault && go build -o /usr/local/bin/socialweb-tenantcredential ./cmd/socialweb-tenantcredential'

FROM gobuild AS credentials

RUN --mount=type=bind,from=goroot,target=/go/toolchain,ro \
    --mount=type=bind,from=gomod,target=/host-mod,ro \
    --mount=type=cache,id=gomod,target=/go/pkg/mod \
    --mount=type=cache,id=gocache,target=/go/cache \
    sh -ec 'mkdir -p /downloads; for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do os=${target%/*}; arch=${target#*/}; GOOS=$os GOARCH=$arch go build -ldflags "-s -w" -buildvcs=false -o /downloads/socialweb-credential-$os-$arch ./cmd/socialweb-credential; done; ls -la /downloads'

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

COPY --from=externalsecrets /usr/bin/external-secrets /usr/local/bin/external-secrets

COPY --from=esobundle /eso/external-secrets.yaml /opt/socialweb/eso/external-secrets.yaml

ENV PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    HOME=/home/kcp

WORKDIR /src

USER 1000:1000

CMD ["socialweb-computer-kcp-check-host"]

FROM controlplane AS k8s

ARG DENO_VERSION=2.9.3

# The release the guests install for the iroh transport, so the door and the
# guests speak one dumbpipe. ensureDumbpipe prefers a dumbpipe already on PATH
# and only fetches when there is none, and its PATH probe is `which` -- which
# this image did not carry either, so the probe threw before it could fetch
# anything and the door was left with no transport at all.
ARG DUMBPIPE_VERSION=0.39.0

USER root

RUN dnf install -y --setopt=install_weak_deps=False yq iproute socat openssl unzip openssh-clients which \
 && dnf clean all \
 && curl -fsSL "https://github.com/denoland/deno/releases/download/v${DENO_VERSION}/deno-x86_64-unknown-linux-gnu.zip" -o /tmp/deno.zip \
 && unzip -o /tmp/deno.zip -d /usr/local/bin \
 && rm /tmp/deno.zip \
 && chmod 0755 /usr/local/bin/deno \
 && curl -fsSL "https://github.com/n0-computer/dumbpipe/releases/download/v${DUMBPIPE_VERSION}/dumbpipe-v${DUMBPIPE_VERSION}-linux-x86_64.tar.gz" -o /tmp/dumbpipe.tar.gz \
 && tar -xzf /tmp/dumbpipe.tar.gz -C /tmp \
 && install -m 0755 /tmp/dumbpipe /usr/local/bin/dumbpipe \
 && rm -f /tmp/dumbpipe.tar.gz /tmp/dumbpipe \
 && test -x /usr/local/bin/dumbpipe \
 && chmod 1777 /tmp

# /tmp is 1777 in the base image and this stage's dnf transaction re-created it
# 0755 root:root. The container runs as uid 1000, so an unwritable /tmp is not a
# missing convenience: install-provider.sh mktemp's its rendered manifests there,
# fails before it applies anything, and the entrypoint exits -- and docker
# restarts the container, in a loop, every thirteen seconds.

COPY --from=gobuild /usr/local/bin/socialweb-gateway /usr/local/bin/socialweb-gateway

COPY --from=gobuild /usr/local/bin/socialweb-credential /usr/local/bin/socialweb-credential

COPY --from=gobuild /usr/local/bin/socialweb-clusterprovider /usr/local/bin/socialweb-clusterprovider

COPY --from=gobuild /usr/local/bin/socialweb-cloud-controller-manager /usr/local/bin/socialweb-cloud-controller-manager

COPY --from=gobuild /usr/local/bin/socialweb-vault /usr/local/bin/socialweb-vault
COPY --from=gobuild /usr/local/bin/socialweb-tenantcredential /usr/local/bin/socialweb-tenantcredential

COPY --from=k8stree / /opt/org-root/

COPY --from=credentials /downloads/ /opt/org-root/socialweb-computer-kcp/socialweb-computer-k8s/web/downloads/

COPY deploy/ /opt/socialweb/deploy/

COPY deploy/container-entrypoint.sh /usr/local/bin/container-entrypoint

ENV DENO_DIR=/opt/deno-dir \
    SOCIALWEB_CONTAINER_DOOR_DIR=/opt/org-root/socialweb-computer-kcp/socialweb-computer-k8s

RUN install -d -o kcp -g kcp -m 0755 /opt/deno-dir \
 && chown -R kcp:kcp /opt/org-root \
 && install -d -o kcp -g kcp -m 0700 /var/lib/socialweb /var/lib/socialweb/kcp /var/lib/socialweb/mint /var/lib/socialweb/clusterprovider /var/lib/socialweb/door \
 && chmod 0755 /usr/local/bin/container-entrypoint /usr/local/bin/socialweb-gateway /usr/local/bin/socialweb-credential /usr/local/bin/socialweb-clusterprovider /usr/local/bin/socialweb-cloud-controller-manager /usr/local/bin/socialweb-vault /usr/local/bin/socialweb-tenantcredential

COPY --from=meshd --chown=kcp:kcp socialweb-meshd-x86_64 socialweb-meshd-x86_64.sha256 socialweb-meshd-aarch64 socialweb-meshd-aarch64.sha256 /opt/org-root/socialweb-computer-kcp/socialweb-computer-k8s/web/downloads/

RUN cd /opt/org-root/socialweb-computer-kcp/socialweb-computer-k8s/web/downloads \
 && sha256sum -c socialweb-meshd-x86_64.sha256 socialweb-meshd-aarch64.sha256

USER 1000:1000

RUN --mount=type=cache,id=deno,target=/opt/deno-dir,uid=1000,gid=1000 \
 cd "$SOCIALWEB_CONTAINER_DOOR_DIR" \
 && deno install --entrypoint hono-socialweb-computer-k8s/mod.ts \
 && deno cache hono-socialweb-computer-k8s/undici-preload.ts hono-socialweb-computer-k8s/mod.ts \
 && ls web/downloads

ENTRYPOINT ["/usr/local/bin/container-entrypoint"]

CMD []
