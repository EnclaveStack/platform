# L2-007 · v0.5 "VMs and Volumes" PRD

---
| Field | Value |
|---|---|
| Document ID | L2-007-v0-5-vms-and-volumes-prd-v1 |
| Layer | L2 — Product |
| Status | Draft |
| Version | v2 |
| Created | 2026-10-21 |
| Last Updated | 2026-10-21 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

This PRD sets what v0.5 adds so that a team can run whole computers and keep data on disks, the way they would with EC2 and EBS on AWS or Compute Engine and persistent disks on GCP:
- virtual machines with an operating system image, a size, SSH keys and a first-boot script;
- volumes: disks that outlive what they're attached to, for VMs and for apps;
- snapshots: point-in-time copies of a volume or a VM's disk, which new volumes and new VMs can start from.

It traces to the roadmap in `L1-002` §4 (v0.5) and builds on:
- `L2-004`: the lifecycle rules apply to every new kind unchanged;
- `L2-006`: VMs sit in networks, answer to private DNS names and obey firewalls exactly as containers do.

Two product decisions were taken with the owner on 2026-10-21:
- VMs run on QEMU on both macOS and Linux, so one engine serves both (`L3-003` ADR-025).
- Volumes attach to VMs (as disks) and to apps and jobs (as folders), so apps can keep files across deploys.

The surface requirements live in their own PRDs:
- console: `L2-001` CON-32 – CON-36;
- CLI: `L2-002` CLI-24 – CLI-28;
- OpenTofu: `L2-003` TF-22 – TF-26.

Engineering: `L3-002` §8, `L3-003` ADR-025 – ADR-028, `L3-004` DAR-017 – DAR-020, `L3-005` §7.

## 1 · The user story

> A developer runs `web` and `api` on EnclaveStack v0.4 in a private network `backend`. They now need a full Linux machine, `worker`, to run a tool that doesn't fit in a container: Ubuntu, 2 CPUs, 4 GiB of memory, their SSH key, and a first-boot script that installs the tool. It belongs in `backend`, where `api` reaches it as `worker.vm.internal`, and a firewall lets only `api` reach its port 9000. It needs a 50 GiB data disk that survives if the VM is rebuilt. Separately, `web` stores user uploads, which today vanish on every deploy; they want those in a volume. Before an upgrade, they snapshot both, and if the upgrade goes wrong they start a fresh volume from the snapshot. They want to do all of this from any surface, and have OpenTofu describe it all.

## 2 · New kinds and capabilities

| Kind / capability | What the user gets | Replaces (AWS / GCP) | Required input | Optional input | What the user reads back |
|---|---|---|---|---|---|
| `vm` (new kind) | A virtual machine running a full operating system on this machine, with its own disk | EC2 instance / Compute Engine VM | name, image, at least one SSH public key | CPUs (default 1), memory (default 1024 MiB), root disk size (default 10 GiB), first-boot script (cloud-init), network and subnet, data disks, ports to publish, power (`running` default, or `stopped`) | power state, private IP, private DNS name, SSH address and user (`enclave`), image, boot console log |
| Images (capability of `vm`) | Ready-made operating system images, downloaded once and reused; or any cloud image by URL; or a snapshot | AMIs / public images | — | catalog name (`ubuntu-24.04`, `debian-12`), `https://` URL of a cloud image, or `snapshot/<name>` | image in use, its checksum |
| `volume` (new kind) | Storage that lives on its own. A `disk` volume is an extra drive for one VM. A `filesystem` volume is a folder mounted into apps and jobs, shared by all their containers | EBS / persistent disk; EFS / Filestore | name, type (`disk` or `filesystem`) | size in GiB (disks; default 10), source snapshot | attached to, size, used space (filesystems) |
| Volume mounts (capability of app and job) | An app or job mounts filesystem volumes at paths of its choice; files there survive deploys, restarts and replicas | ECS EFS volumes / Cloud Run volume mounts | — | `volumes`: volume + path + read-only | — |
| `snapshot` (new kind) | A point-in-time copy of a volume or a VM's root disk, kept on this machine, independent of its source | EBS snapshots / disk snapshots, AMIs from snapshots | name, source (`volume/<name>` or `vm/<name>`) | — | size, source type, time taken |

## 3 · Requirements

### VMs

| ID | Requirement | Priority |
|---|---|---|
| VM-01 | A VM has 1 – 16 CPUs (never more than the machine has) and 512 – 65536 MiB of memory (never more than the machine's memory minus 2 GiB). CPUs and memory can change; the change applies by restarting the VM, which the update says before it happens | Must |
| VM-02 | A VM boots from an image (IMG-*) onto its own root disk of 5 – 2048 GiB (default 10). The root disk grows in place while the VM runs, and the root filesystem takes the new space on the next boot; it never shrinks. Changing the image is a replacement (CP-04) | Must |
| VM-03 | A VM takes 1 – 10 SSH public keys (inline, or a secret holding one key per line). No password login exists. The keys are installed for a user `enclave` with administrator rights (`sudo`), the same on every image, and for the image's own default user (`ubuntu`, `debian`, …). Keys are set at the first boot and can't change afterwards, as with an EC2 key pair (CP-04): different keys mean a new VM | Must |
| VM-04 | A VM takes an optional first-boot script, up to 64 KiB: a cloud-init document (`#cloud-config`) or a shell script (`#!`). It runs once, on the first boot, and can't change afterwards (CP-04) | Must |
| VM-05 | A VM is reachable over SSH from this machine without any setup: EnclaveStack publishes its port 22 on a free port of the loopback interface and reports `sshAddress` (`127.0.0.1:2201`) and the user | Must |
| VM-06 | `power` is `running` (default) or `stopped`. `start`, `stop` and `restart` actions change it. Stopping asks the operating system to shut down and forces it off after 60 s. A stopped VM keeps its disks and uses no CPU or memory | Must |
| VM-07 | A VM that shuts itself down (`poweroff` inside it) becomes `stopped` and says so in its events; it is not started again. A VM whose process crashes is started again (CP-10) | Must |
| VM-08 | VMs survive the control plane: restarting EnclaveStack doesn't restart them. After the machine itself reboots, VMs whose power is `running` start again (CP-09) | Must |
| VM-09 | The boot console (serial output) is readable like app logs (CP-14), so a VM that doesn't boot can be diagnosed without SSH | Must |
| VM-10 | `publish` lists up to 10 ports the VM serves on the same host port, on every interface, as an app's `publish` does. Port 22 can't be published this way (VM-05 covers SSH) | Should |
| VM-11 | A VM runs only if the machine can run VMs: QEMU 8 or later installed, and hardware virtualization (Hypervisor framework on macOS, `/dev/kvm` on Linux). If not, the machine reports why with the command to fix it ("Install QEMU: brew install qemu"), and VMs stay `pending` with that message instead of failing | Must |
| VM-12 | Deleting a VM deletes its root disk. Its data disks are detached and kept (CP-19 says so before confirming) | Must |

### VM images

| ID | Requirement | Priority |
|---|---|---|
| IMG-01 | A catalog of images is built in: `ubuntu-24.04` and `debian-12`, each for the machine's own architecture (arm64 or amd64). The catalog pins each image's download address and checksum | Must |
| IMG-02 | An image is downloaded once and reused by every VM that starts from it. A download that fails is retried with backoff (CP-07); one whose checksum doesn't match is a permanent failure | Must |
| IMG-03 | An image may be any cloud image by `https://` URL (qcow2 or raw, with cloud-init), with an optional `sha256` checksum. The image must match the machine's architecture: there is no emulation | Must |
| IMG-04 | A VM may start from `snapshot/<name>` of another VM's root disk: a copy of that machine, like launching from an AMI. The new VM still runs its own first-boot (new SSH keys, new host name) | Should |
| IMG-05 | `GET /machine` lists the cached images with their size; images no VM uses are removed after 7 days | Should |

### VMs in networks

| ID | Requirement | Priority |
|---|---|---|
| VMN-01 | A VM takes `network` and `subnet` like an app (NET-03): without them, it's on the default network. It gets one private IP there and keeps it while it exists | Must |
| VMN-02 | A VM answers to `<name>.vm.internal` inside its network (NET-07) and reaches other resources by their private DNS names | Must |
| VMN-03 | A VM can be a firewall's target and a source in a rule (`vm/<name>`), with the same meaning as for containers (FW-01 – FW-09) | Must |
| VMN-04 | A VM in a network with `internet: false` has no route to the internet and can't publish ports (NET-05). Its SSH address on the loopback interface still works | Must |
| VMN-05 | Moving a VM to another network or subnet gives it a new private IP there within seconds, without restarting it. Open connections are dropped | Should |
| VMN-06 | A domain or a load balancer can send traffic to a VM's port (`vm/<name>` as the target), with health checks as for apps | Should |

### Volumes

| ID | Requirement | Priority |
|---|---|---|
| VOL-01 | A `disk` volume is 1 – 2048 GiB. It is attached to at most one VM, which names it in `disks` (up to 8). Inside the VM it appears as an extra drive with a stable name, `/dev/disk/by-id/virtio-<volume name>` (the name cut to 20 characters, so two disks of one VM must differ in their first 20). EnclaveStack never formats or mounts it | Must |
| VOL-02 | Attaching or detaching a disk on a running VM restarts the VM, and the update says so first | Must |
| VOL-03 | A disk grows in place, while its VM runs; the VM sees the new size at once. It never shrinks | Must |
| VOL-04 | A `filesystem` volume is a folder mounted into apps and jobs that name it in `volumes` with an absolute path (and `readOnly`, default false). Several apps, all their replicas and both revisions during a deploy see the same files | Must |
| VOL-05 | Adding, removing or moving an app's mount deploys a new revision (DEP-01) | Must |
| VOL-06 | A volume reports what it's attached to and its size; a filesystem volume reports the space its files use | Must |
| VOL-07 | Deleting a volume that a VM, app or job uses is refused with them listed (CP-12). Deleting a volume deletes its data (CP-19) | Must |
| VOL-08 | A volume can start from a snapshot (`snapshot: <name>`) of the same type: a disk from a disk or VM snapshot, a filesystem from a filesystem snapshot. A disk started this way is at least the snapshot's size | Must |
| VOL-09 | Databases and caches keep managing their own storage as in v0.3; they can't mount volumes | Must |

### Snapshots

| ID | Requirement | Priority |
|---|---|---|
| SNP-01 | A snapshot copies a volume or a VM's root disk while it is in use. A disk snapshot holds what was on the disk at one moment, as after a power cut (crash-consistent). A filesystem snapshot copies file by file while apps keep running: a file written during the copy may be captured part-way, and the snapshot says so | Must |
| SNP-02 | A snapshot is a full, independent copy: deleting its source, or the snapshot itself, never affects the other | Must |
| SNP-03 | A snapshot is never modified. Creating one again with the same name requires deleting the old one first | Must |
| SNP-04 | A snapshot reports its source, its type (`disk` or `filesystem`) and size, and becomes `ready` when the copy is complete | Must |
| SNP-05 | A snapshot of a disk taken while its VM runs doesn't pause the VM for more than a second | Should |
| SNP-06 | Deleting a snapshot that a VM's image or a volume's source names is allowed: they already hold their own copy (SNP-02). Their `snapshot` field keeps showing where they came from | Must |

## 4 · Applies to every new kind

`L2-004` CP-01 – CP-19 apply unchanged to `vm`, `volume` and `snapshot`:
- one API, asynchronous phases, idempotent creates, `If-Match`, retry and backoff;
- crash recovery, events, and deletion rules;
- parity across the console, CLI and OpenTofu.

`GET /machine` (CP-16) also reports whether VMs can run (VM-11) and the CPUs and memory reserved by running VMs.

## 5 · Order of delivery

1. **Filesystem volumes and their snapshots.** Apps keep files; no QEMU needed.
2. **VMs.** The QEMU engine, images, first boot, SSH, power and boot console.
3. **VMs in networks.** Private IPs, private DNS, firewalls, `internet: false`; domains and load balancers last.
4. **Disk volumes and disk snapshots.** Data disks, snapshots of disks and VMs, VMs started from snapshots.

**Delivery status (2026-10-21):** phases 1 – 4 are built and tested on every surface, on real Docker and real QEMU (Apple M4, HVF); v0.5 is not released yet. VMN-06 (domains and load balancers in front of a VM) is not built.

| Phase | Requirements | Built | Where it's recorded |
|---|---|---|---|
| 1 · Filesystem volumes + snapshots | VOL-04 – VOL-09, SNP-01 – SNP-04, SNP-06 | Yes | ADR-027, ADR-028 |
| 2 · VMs | VM-01 – VM-12, IMG-01 – IMG-05 | Yes, on macOS with HVF; Linux with KVM runs in CI from v0.5 on, not yet observed | ADR-025 |
| 3 · VMs in networks | VMN-01 – VMN-05 | Yes. VMN-06 (Should) is not built | ADR-026 |
| 4 · Disk volumes + disk snapshots | VOL-01 – VOL-03, SNP-05, IMG-04 | Yes | ADR-027, ADR-028 |

**Measured on the live walk-through** (`L5-001` §2c, Apple M4, Docker Desktop 29.7.2, QEMU 11.1.2):
- the first VM was ready in 92 s, including the first download of the Debian image (341 MB);
- a VM's link removed outside EnclaveStack was back in 10 s, without restarting the VM;
- a killed QEMU process was started again and answered SSH within 65 s, boot included;
- a disk snapshot taken while the VM wrote to it took 0.6 s;
- restarting the control plane kept the same QEMU process running (adopted).

**As built:**
- IMG-03: an image for another architecture can't be detected before it boots. It is accepted, and the VM's boot console shows why it doesn't start.
- IMG-01: Debian publishes only SHA-512 sums, so the catalog checks Debian images with SHA-512 and Ubuntu images with SHA-256. `imageSha256` (URL images) stays SHA-256.
- IMG-04: a VM from a snapshot needs a root disk at least as big as the snapshot; a smaller one is refused with the size to use.
- VOL-06: `attachedTo` is refreshed by the drift pass (about once a minute), not at the moment something mounts the volume.
- VM-05: `enclave vm ssh` files each VM's host key under the VM's ID (`HostKeyAlias`), because loopback ports are reused by other VMs over time.

## 6 · Out of scope for v0.5

- Live attach or detach of disks without a restart; live migration; memory snapshots (VM-07, VOL-02 restart instead).
- Emulating another architecture (arm64 images on amd64 machines or the reverse).
- Windows guests and GPUs passed through to VMs.
- Scheduled snapshots, and snapshots copied to buckets (backups of volumes); snapshots reside on this machine only.
- Quotas on filesystem volumes; filesystem volumes shared with VMs.
- Autoscaling groups of VMs and instance templates.
- Several machines (v0.8).

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-21 | Soupler | v0.5 scope: vm, volume and snapshot kinds; images, VMs in networks, volume mounts for apps and jobs (VM, IMG, VMN, VOL, SNP requirements). Engine and volume scope decided with the owner |
| v2 | 2026-10-21 | Soupler | Delivery status: phases 1 – 4 built and tested on every surface (VMN-06 not built); live measurements; as-built notes |
