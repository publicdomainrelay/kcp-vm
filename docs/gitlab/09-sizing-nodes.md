# 09 - Sizing a node for a real job

A node is a guest. Default is 2 vCPU, 2048 MiB, 16 GiB disk, which fits a probe and nothing else.
A job with a toolchain in it needs four numbers, and they come from three different places.

| What | Where | Why there |
| --- | --- | --- |
| `vcpu`, `memMiB` | the node image's builder config | a node's boot shape is a property of the image it boots |
| `rootfs_headroom_mib` | the node image's builder config | sizes the root filesystem, which is where containerd lives |
| `SOCIALWEB_CLUSTERPROVIDER_NODE_DISK_MIB` | clusterprovider env | per node, at provision time |

`vcpu` and `memMiB` are fold into the image's fingerprint, so changing either builds a NEW image
under a NEW name. The old one keeps its name and is never asked for again.

## The one that is easy to miss

**The root filesystem is where the job's image and its build outputs have to fit.** Not the node
disk. The node disk is attached and mounted READ-ONLY as the identity disk, so it cannot hold k3s
state - `SOCIALWEB_CLUSTERPROVIDER_NODE_DISK_MIB` does not help here.

The root filesystem is `rootfs.ext4`, and its size is `du(stage) + rootfs_headroom_mib`. It is
cloned per node. Measured, both ways:

| Setting | Result |
| --- | --- |
| default headroom | pull of a 9 GB image dies: `failed to pull and unpack image ... failed to copy: write /var/lib/rancher/k3s/agent/containerd/io.containerd.content.v1.content/ingest/...: no space left on device` |
| `rootfs_headroom_mib: 55000` | root filesystem is 58,401,488,896 bytes and the pull completes |

Read the failure right: `no space left on device` while PULLING is the root filesystem, and it
arrives as a Pod that never starts, not as a disk that is full.

## A recipe for a full toolchain build

Node image config (the operator's `nodeimage.json`, or a copy of it):

```json
{
  "vcpu": 12,
  "memMiB": 24576,
  "rootfs_headroom_mib": 55000
}
```

```bash
socialweb-nodeimage -config /path/to/nodeimage.json
```

Pull requests must fit the node too. The Pod is sized by the runner, see
[05](05-runner-config.md): `memory_limit` over node size is fine, `memory_request` is not.

## A recipe for a scale check

Five nodes at once, so five of the above is 60 vCPU and 120 GiB and does not fit a workstation.

```json
{
  "vcpu": 2,
  "memMiB": 3072,
  "rootfs_headroom_mib": 1500
}
```

and drop the Pod requests to `100m`/`128Mi`. Requests are what the scheduler places against;
limits are not placement.

## The state directory has to be on real disk

The images built for a node are written where the harness puts its state, and a root filesystem of
58 GB is a 58 GB file plus the node's disk. On a host where `/tmp` is a tmpfs, that is RAM:

```
TMPDIR=/var/tmp/kcp    # or anywhere on the host's real disk
```

A state directory that cannot hold a node's disks fails as a full filesystem partway through a
boot, not as a configuration error.

## What does NOT go in the image

A file of about 2 GiB or more cannot be staged into the node image. `mkfs.ext4 -d` refuses it:

```
__populate_fs: Ext2 file too big while writing file "127.0.0.1-5000-...tar"
mkfs.ext4: Ext2 file too big while populating file system
```

Measured: a 3.3 GB tarball is refused at 6 GB and at 12 GB, alone and with the whole stage, with
default block size and with 4 KiB blocks; a 3.5 GB file of zeros is accepted in a 4.9 GB
filesystem. So it tracks the file, not the filesystem size. Anything that big - a builder image,
a large model - is pulled by the guest instead. See [07](07-job-images.md).

## Check before you boot

```bash
go run ./cmd/socialweb-computer-kcp-check-host -config /path/to/nodeimage.json
```

One summary line, three categories: HOLDS, MISSING, MUST BE PASSED IN.
