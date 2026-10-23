# L2-010 · v0.8 "Many machines" PRD

---
| Field | Value |
|---|---|
| Document ID | L2-010-v0-8-many-machines-prd-v1 |
| Layer | L2 — Product |
| Status | Draft |
| Version | v3 |
| Created | 2026-10-22 |
| Last Updated | 2026-10-23 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

This PRD sets what v0.8 adds, so that one EnclaveStack can spread over several computers, the way AWS spreads over availability zones and GCP over zones, and so that common products install in one step, as from AWS Marketplace or a Quick Start:
- machines: more computers joined to one EnclaveStack, each running a small agent;
- placement: every resource that runs something runs on a machine, chosen by EnclaveStack or by the user;
- networks across machines: private networks, addresses and `.internal` names work the same wherever a resource runs;
- recovery: what happens when a machine goes offline and comes back;
- moving: a resource moves to another machine with its data;
- catalogue: ready-made stacks (WordPress, Ghost, n8n and others) and the user's own templates, installed as one `stack`.

It traces to the roadmap in `L1-002` §4 (v0.8) and builds on:
- `L2-004`: the lifecycle rules apply to every new kind unchanged;
- `L2-006`: networks, subnets, firewalls and load balancers, which now span machines;
- `L2-007`: VMs and volumes, which now live on a machine and can move;
- `L2-009`: projects, roles and alerts.

Product decisions taken with the owner on 2026-10-22:
- **Private networks span machines.** An app on one machine reaches a database on another by the same private address and `.internal` name it would use on one machine. Traffic between machines goes through an encrypted WireGuard tunnel (`L3-003` ADR-045).
- **When a machine goes offline**, apps without data restart on another machine after a minute. Databases, caches, volumes, VMs and anything pinned to the machine wait for it, because their data is on its disk (ADR-046).
- **The catalogue** has built-in stacks and the machine's own templates, added from a file. Installing one creates a `stack` that groups its resources; deleting the stack deletes them (ADR-048).
- Taken as given and stated to the owner: the machine EnclaveStack was installed on stays the only control plane. Other machines run an agent that only does what the control plane asks. A move of a resource with data stops it, copies the data and starts it on the new machine, with a short interruption; there is no live migration.

The surface requirements live in their own PRDs:
- console: `L2-001` CON-54 – CON-60;
- CLI: `L2-002` CLI-46 – CLI-52;
- OpenTofu: `L2-003` TF-42 – TF-47.

Engineering: `L3-002` §11, `L3-003` ADR-042 – ADR-048, `L3-004` DAR-034 – DAR-039, `L3-005` §10, `L3-006` §10, `L3-007` §13.

## 1 · The user story

> The shop from v0.7 outgrows one Mac mini. The team buys a second-hand Linux box (`rack-1`) and adds it from the console: EnclaveStack shows a command, they run it on the box, and a minute later `rack-1` is online with its CPUs and memory. Their OpenTofu module adds `machine = "rack-1"` to the `orders` database and the `search` service; the database moves over during a quiet hour with half a minute of downtime, keeping its private address `orders.database.shop.internal`, so `api` on the Mac never notices. New apps land wherever there is most free memory. When someone unplugs `rack-1` to move a shelf, the stateless `api` replicas that ran there come back on the Mac within two minutes; the database waits, and is back as soon as the box is. Marketing wants a blog: they open the catalogue, pick WordPress, give it the name `blog` and the domain `blog.shop.example`, and get a running WordPress with its MySQL database, its uploads volume and HTTPS, as one `stack` they can delete again in one step. The team's own "shop worker" setup (an app, a queue and a cache) is saved as a template, so every new project gets it the same way.

## 2 · New kinds and capabilities

| Kind / capability | What the user gets | Replaces (AWS / GCP) | Required input | Optional input | What the user reads back |
|---|---|---|---|---|---|
| Machines | More computers in one EnclaveStack | Availability zones / zones | name | labels, schedulable | join command; state, address, CPUs, memory, engine, VMs, last seen, resources |
| Placement (capability of apps, databases, caches, jobs, load balancers, VMs, volumes, search and email services) | Where a resource runs | Choosing an AZ / zone | — | `machine`, `machineLabels` | the machine it runs on |
| Networks across machines (capability of networks) | One private network over all machines | A VPC across AZs | — | — | each machine's link to the others |
| Moving (capability of placed kinds) | A resource moves to another machine with its data | — (re-creating in another AZ) | the new `machine` | — | progress, downtime |
| Catalogue | Ready-made stacks and the machine's own templates | AWS Marketplace, Quick Starts / Cloud Marketplace | — | own templates (file) | entries, parameters |
| `stack` (new kind) | A product installed from the catalogue, as one thing | CloudFormation stacks / Deployment Manager | name, template, parameters | template version | members, their phases, outputs |

## 3 · Requirements

### Machines

| ID | Requirement | Priority |
|---|---|---|
| MCH-01 | The computer EnclaveStack was installed on is the **main machine**. It is listed as a machine like any other: an ID made at first start (`mch_…`, stored with its data, never changed), a name that `--machine` refers to (made from the computer's own name, lowercase letters, digits and hyphens, and fixed once set), and a **display name** for people (free text). `main` keeps working as a shortcut for the main machine in commands, specs and the API. Everything that existed before v0.8 runs on it. It can't be removed. *Planned: the ID and name scheme; today both are the word `main`* | Must |
| MCH-10 | The **display name** of a machine defaults to the computer's own name (setup offers it, and it can be changed there). A machine admin can **rename it** from the machine's page. Only the display name changes: the ID and the name used in commands stay the same. The machine's page shows the ID in small grey text beside the display name | Must |
| MCH-02 | A machine admin **adds a machine** by giving it a name (the resource name rules) and optional labels (`key=value`, up to 10). EnclaveStack returns a one-time join code, valid one hour, and the command to run on the other computer: `enclave agent join <main address> --code <code>` | Must |
| MCH-03 | The other computer needs Linux or macOS with Docker, and QEMU for VMs (as `L2-007` VM-11). From the main machine it must be reachable on the agent port (7071/tcp, configurable). The machines must reach each other on the tunnel port (51820/udp, configurable). It must reach the main machine's API once, to join | Must |
| MCH-04 | **Joining** uses the code once. The agent makes its own key, which never leaves the computer, and gets a certificate from the main machine. From then on the main machine and the agent only talk over mutual TLS: the agent obeys only the main machine, and the main machine only accepts the computer that joined under that name | Must |
| MCH-05 | A machine shows its **state** (`waiting` for the agent, `online`, `offline`), address, operating system and architecture, engine version, agent version, CPUs, memory total and reserved, VM support, when it was last seen, its tunnel links to the other machines, and what runs on it | Must |
| MCH-06 | The main machine checks each machine every 10 seconds. A machine that hasn't answered for 30 seconds is **offline**; an event is recorded on it and on what runs there, and an alert with the new condition **machine offline** fires (`L2-009` ALR-02) | Must |
| MCH-07 | A machine can be marked **not schedulable**: nothing new is placed there, and nothing moves there, but what runs there keeps running | Must |
| MCH-08 | **Draining** a machine marks it not schedulable and moves everything on it to other machines (MOVE-*). Pinned resources are moved only when their `machine` is changed; the drain lists them | Should |
| MCH-09 | **Removing** a machine is refused while anything runs on it; the refusal lists it. `force` removes an offline machine anyway: what ran there becomes `failed` with "the machine `<name>` was removed", for the user to restore from backups. The machine's certificate stops working at once. `enclave agent leave` on that computer removes EnclaveStack's containers and the agent's identity | Must |
| MCH-10 | The agent and the main machine must run the same minor version. A mismatch is shown on the machine and nothing new is placed there until it's upgraded | Must |
| MCH-11 | Only machine admins add, change, drain and remove machines. Everyone who can see the machine can read the machine list; placing a resource on a machine needs the editor role in its project | Must |
| MCH-12 | `enclave agent join` can install the agent as a service that starts with the computer (systemd on Linux, launchd on macOS) | Should |

### Placement

| ID | Requirement | Priority |
|---|---|---|
| PLC-01 | Apps, databases, caches, image jobs, load balancers, VMs, volumes, search services and email services run on one machine each. They take an optional `machine` (a machine's name) and optional `machineLabels` (labels the machine must have) | Must |
| PLC-02 | Without `machine`, EnclaveStack **chooses** one when the resource is created: among online, schedulable machines with the right version, that have the labels, can run the kind (VMs need VM support) and have the memory free (`L2-004` CP-16), the one with the most free memory; the main machine on a tie. If none fits, the resource stays `pending` and its message says why ("No machine has 512 MiB free", "No machine has the label disk=ssd") and it is placed as soon as one does | Must |
| PLC-03 | A resource keeps its machine. Changing `machine` moves it (MOVE-*). Changing `machineLabels` so that its machine no longer has them moves it to one that does | Must |
| PLC-04 | What uses a volume runs where the volume is: an app or job that mounts a filesystem volume, and a VM with disk volumes, follow the volume's machine. Their own `machine`, if given, must be the same, or the request is refused naming the volume. Two volumes on different machines can't be used together. An app's job runs on the app's machine | Must |
| PLC-05 | Every placed resource reports `machine` in its outputs. Its published ports bind on that machine, and `host` is that machine's address | Must |
| PLC-06 | Buckets, queues, topics, domains, CDNs, transcoders, DNS zones and records stay on the main machine. Domains, CDNs and load balancers reach apps on any machine. Backups take their dump on the database's machine | Must |
| PLC-07 | Images are pulled on the machine that runs them; an app built from Git is built on its machine | Must |
| PLC-08 | With one machine, nothing changes: no field is needed, and every output keeps its value | Must |

### Networks across machines

| ID | Requirement | Priority |
|---|---|---|
| XNET-01 | Every network, and the default network, exists on every online machine with the same address range. Resources in a network reach each other by private IP and `.internal` name whichever machines they run on. Networks stay isolated from each other as in `L2-006` NET-03 | Must |
| XNET-02 | No two resources get the same private address, whichever machines they run on | Must |
| XNET-03 | Firewalls work across machines unchanged: traffic arrives with its real source address, so rules naming resources, networks or ranges match | Must |
| XNET-04 | Traffic between machines is encrypted and authenticated (WireGuard). Each machine makes its own tunnel key; the main machine hands out the public keys | Must |
| XNET-05 | A network with `internet: false` has no route out on any machine | Must |
| XNET-06 | Each machine shows its link to every other machine: connected or not, and when it last heard from it | Must |
| XNET-07 | A new resource on one machine is reachable from the others before it takes traffic: apps before their health check passes, everything else before it is `ready` | Must |
| XNET-08 | Between two machines on a gigabit network, an app reaches another app at 300 Mbit/s or more, adding less than 1 ms of latency, measured (`L5-003`) | Should |

### When a machine goes offline

| ID | Requirement | Priority |
|---|---|---|
| FAIL-01 | What runs on an offline machine keeps its phase; its message says "`<machine>` is offline since `<time>`", and the console marks it | Must |
| FAIL-02 | An app with no volume and no `machine` of its own is started on another machine when its machine has been offline for 60 seconds: a new revision is deployed there, with an event naming both machines. An image job's next runs go to another machine | Must |
| FAIL-03 | Databases, caches, search and email services, volumes, VMs, load balancers and pinned resources wait for their machine. Changing their `machine` while it's offline is refused, because their data can't be read; the message points to restoring a backup | Must |
| FAIL-04 | When the machine comes back, what moved away is removed from it, so nothing runs twice, and what waited carries on with its data | Must |
| FAIL-05 | An outage shorter than 60 seconds moves nothing | Must |
| FAIL-06 | While the main machine is down, the other machines keep running what they have; nothing can be changed and domains aren't served until it is back | Must |

### Moving a resource

| ID | Requirement | Priority |
|---|---|---|
| MOVE-01 | An **app** moves like a deploy: a new revision starts on the new machine, takes traffic once healthy, and the old one stops. With the rolling strategy there's no interruption | Must |
| MOVE-02 | A **database, cache, search or email service** is stopped, its data copied to the new machine, and started there. Its private address, `.internal` name, password and data are kept. The events say how long it was unavailable | Must |
| MOVE-03 | A **volume** copies its files (filesystem) or its disk (disk) to the new machine. What uses it moves with it: apps and jobs are deployed there and VMs are shut down, moved and started again | Must |
| MOVE-04 | A **VM** shuts down, its root disk and disk volumes are copied, and it starts on the new machine with the same private address and name | Must |
| MOVE-05 | A **load balancer** is started on the new machine, then the old one is removed | Must |
| MOVE-06 | A move copies before it switches: if it fails, the resource carries on where it was, and its message says why. Steps show the bytes copied | Must |
| MOVE-07 | Published ports are bound on the new machine, so `host` and `port` change; private addresses don't | Must |

### Catalogue and stacks

| ID | Requirement | Priority |
|---|---|---|
| CAT-01 | The **catalogue** lists stacks: each has a name, title, description, version, its parameters (name, title, type — `string`, `number`, `boolean`, `secret`, `hostname` — default, required) and the resources it creates | Must |
| CAT-02 | Built-in entries: **WordPress** (MySQL, a volume for uploads), **Ghost** (MySQL, a volume), **n8n** (PostgreSQL, a volume), **Uptime Kuma** (a volume), **Gitea** (PostgreSQL, a volume) and **Plausible Analytics** (PostgreSQL, ClickHouse). Images are pinned by version. Each takes an optional `domain` parameter that adds a domain with HTTPS | Must |
| CAT-03 | A machine admin **adds their own template** from a YAML or JSON file in the same format (`L3-002` §11.6), and removes it. Own templates are listed next to the built-ins, marked as such. A template with an error is refused with the line and the reason | Must |
| CAT-04 | **Installing** creates a `stack` in a project with a name, the template (a catalogue entry, or a template given inline) and parameter values. The stack creates each member as an ordinary resource of the project, named `<stack>` or `<stack>-<member>`, in dependency order. A `secret` parameter, and every password a member needs, becomes a secret of the stack | Must |
| CAT-05 | A stack shows its members with their phases, and the template's outputs (an address, an admin URL). It is `ready` when every member is, `failed` when one fails (its message names it), and in progress otherwise | Must |
| CAT-06 | Members can be restarted, redeployed and read like any resource, but not changed or deleted on their own: that is refused, naming the stack. Changing the stack's parameters, or moving it to a newer version of its template, changes, adds and removes members to match | Must |
| CAT-07 | **Deleting a stack** deletes its members, dependents first. Members with data (databases, volumes) are listed when the console or CLI asks to confirm | Must |
| CAT-08 | A template can only do what its installer could do by hand: every member is checked as if created by them, with the same validation, roles, quotas and placement | Must |
| CAT-09 | Names a template creates can't clash: installing is refused before anything is created when a member's name already exists in the project | Must |

## 4 · Applies to every new kind

`L2-004` CP-01 – CP-19 apply unchanged to `stack`:
- one API, asynchronous phases, idempotent creates, `If-Match`, retry and backoff;
- crash recovery, drift repair, events, and deletion rules;
- parity across the console, CLI and OpenTofu.

Machines are not a kind: like projects, they are machine-wide objects with their own API (`L3-002` §11.2). Projects and roles apply to stacks as to every kind.

## 5 · Order of delivery

1. **Machines and the agent**: join, identity, mutual TLS, health.
2. **Placement**: resources run on the machine chosen for them; everything the control plane does to a resource reaches its machine.
3. **Networks across machines**: the tunnel, names and addresses, firewalls.
4. **Offline machines**: detection, apps restarted elsewhere, clean-up on return.
5. **Moving** resources, and draining machines.
6. **Catalogue and stacks**.

**Delivery status (2026-10-22):** built on all three surfaces (API, CLI, OpenTofu provider, web console), in the order above.
- **Verified by**:
  - the Go suite with the race detector;
  - the real-Docker driver suite, with the two-engine mesh test;
  - the OpenTofu acceptance suite (`TestAccMachinesPlacementAndMoves`, `TestAccStacksAndTemplates`);
  - the console's unit, flow and Playwright suites;
  - `make e2e-machines`: the server in a container and a Docker-in-Docker agent, covering the join, a private network across machines, a move with data, and an app rescheduled off an offline machine.
- **As built**:
  - the main machine carries labels and can be made not schedulable like any other;
  - `machine_offline` alerts exist on every surface (`--machine-offline` in the CLI);
  - each machine has its own metrics, with disk only for the main machine.
- **Not yet walked**:
  - two physical computers over a LAN;
  - the WireGuard throughput measurement (`L5-003`);
  - a VM moved between machines;
  - built-in stacks on a real engine. The six built-ins are validated by rendering, and run on the fake engine.

## 6 · Out of scope for v0.8

- More than one control plane, or the control plane itself moving to another machine (high availability of the control plane).
- Live migration (moving a running VM or database without stopping it); replicated databases across machines.
- Placing buckets, queues, topics or the edge on other machines; several edges.
- Machines on other sites reached over the internet without a direct UDP path (relays, NAT traversal).
- Automatic rebalancing of resources between machines.
- A public, shared catalogue to publish templates to; templates with code (hooks) of their own.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-22 | Soupler | v0.8 scope: machines and the agent, placement, networks across machines, offline machines, moving, catalogue and stacks (MCH, PLC, XNET, FAIL, MOVE, CAT requirements). Networks spanning machines, restarting apps on machine loss, and built-in plus own templates as grouped stacks decided with the owner |
| v2 | 2026-10-22 | Soupler | Delivery status: built on every surface; as-built differences; what is not yet walked |
| v3 | 2026-10-23 | Soupler | MCH-01 reworded: the main machine gets a generated ID and a name from the computer's name, `main` stays as a shortcut (planned); MCH-10: display name defaults to the computer's name, admins rename it, the ID is shown in grey |
