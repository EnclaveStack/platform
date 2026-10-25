# L2-006 · v0.4 "Networking" PRD

---
| Field | Value |
|---|---|
| Document ID | L2-006-v0-4-networking-prd-v1 |
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

This PRD sets what v0.4 adds so that a team can lay out a product's network on EnclaveStack the way they would on AWS or GCP:
- private networks with their own address ranges, isolated from each other;
- private DNS names between resources;
- firewall rules that decide who may talk to what;
- apps running as several replicas behind a load balancer;
- DNS records kept at the team's DNS provider;
- certificates through DNS, including wildcards and machines that can't open port 80.

It traces to the roadmap in `L1-002` §4 (v0.4) and builds on `L2-004` (the lifecycle rules apply to every new kind unchanged) and `L2-005` (domains, revisions). Wildcard certificates through DNS-01 were deferred from v0.3 (`L2-005` §6) and are part of v0.4. The surface requirements live in their own PRDs:
- console: `L2-001` CON-26 – CON-31;
- CLI: `L2-002` CLI-18 – CLI-23;
- OpenTofu: `L2-003` TF-16 – TF-21.

Engineering: `L3-002` §7, `L3-003` ADR-020 – ADR-024, `L3-004` DAR-012 – DAR-016, `L3-005` §6.

## 1 · The user story

> A developer runs `web` and `api` (apps), `orders` (a database) and `sessions` (a cache) on EnclaveStack v0.3. They want the database and cache in a private network that only `api` can reach, on the database port only. `api` must run as three replicas behind a load balancer, and `web` reaches it as `api.app.internal`. `example.com` is hosted at Cloudflare: EnclaveStack should create `api.example.com` there, pointing at the machine, and serve `*.example.com` with one wildcard certificate, even though the machine sits behind a router that doesn't forward port 80. They want to do all of this from any surface, and have OpenTofu describe it all.

Galiaraa's needs map onto this directly: a private data tier, a load-balanced gateway, and records at its DNS provider.

## 2 · New kinds and capabilities

| Kind / capability | What the user gets | Replaces (AWS / GCP) | Required input | Optional input | What the user reads back |
|---|---|---|---|---|---|
| `network` (new kind) | A private network with its own IPv4 range, split into named subnets, isolated from every other network | VPC + subnets / VPC + subnets | name, CIDR | subnets (name + CIDR inside the network; default one subnet `default` covering it all), internet access (default on) | gateway, subnets, number of attached resources |
| Placement (capability of app, database, cache, job) | A resource placed in a network and optionally a subnet; resources without a network stay on the default network, as in v0.3 | ENI placement / subnet choice | — | `network`, `subnet` | private IP, private DNS name |
| Private DNS (capability) | Every resource answers to `<name>.<kind>.internal` inside its network | Cloud Map / Cloud DNS private zones | — | — | `privateDns` |
| `firewall` (new kind) | Allow rules applied to resources: once a resource has a firewall, only what a rule allows reaches it | Security groups / VPC firewall rules | name, targets (resources) | ingress rules (sources, protocol, ports), egress default (`allow` or `deny`) and egress rules | applied to N containers, last applied time |
| Replicas (app capability) | An app runs as 1–10 identical containers; deploys roll all replicas; domains and internal DNS spread requests across them | ECS desired count / Cloud Run instances | — | `replicas` (default 1) | running replicas |
| `loadbalancer` (new kind) | A TCP or HTTP load balancer in front of one or more apps' replicas, with health checks and weights | ALB / NLB / Cloud Load Balancing | name, port, targets (apps) | protocol (default `http`), algorithm, health check, publish on a host port | internal and published address, healthy targets |
| `zone` (new kind) | A DNS zone at an external provider that EnclaveStack may write records into | Route 53 hosted zone / Cloud DNS zone | name, provider (`cloudflare` or `route53`), zone name, credentials secret | — | zone ID, name servers |
| `record` (new kind) | A DNS record kept at the provider, created, updated and deleted with the resource; repaired if changed outside EnclaveStack | Route 53 record set / Cloud DNS record set | name, zone, record name, type | values, or "point at this machine"; TTL (default 300) | fully-qualified name, current values |
| DNS-01 certificates (domain capability) | A domain in a zone gets its certificate through DNS: wildcard hostnames work, and the edge needs no inbound port 80 | ACM DNS validation / Certificate Manager DNS authorization | — | `zone` on the domain | certificate state, issuer, expiry (as v0.3) |

## 3 · Requirements

### Networks and private DNS

| ID | Requirement | Priority |
|---|---|---|
| NET-01 | A network has an IPv4 CIDR from the private ranges (10/8, 172.16/12, 192.168/16, 100.64/10), sized /16 to /28. It may not overlap another EnclaveStack network or the default network | Must |
| NET-02 | A network has 1–16 named subnets, each inside the network's CIDR and not overlapping the others. Without subnets, one subnet `default` covers the whole network. The CIDR and subnets cannot change (CP-04): a different layout is a new network | Must |
| NET-03 | Apps, databases, caches and image-based jobs take an optional `network` and `subnet`. A job that runs with an app's image runs in that app's network. Without `network`, a resource stays on the default network (v0.3 behaviour). Without `subnet`, it gets an address in the network's first subnet | Must |
| NET-04 | Resources in different networks can't reach each other. EnclaveStack's own services (the edge proxy, health checks and the object store) reach every network, so domains, health checks and buckets keep working | Must |
| NET-05 | A network with `internet: false` has no route to the internet. Resources in it can't publish a host port; a domain or a published load balancer is the way in | Must |
| NET-06 | Moving an app to another network or subnet deploys a new revision there (rolling, DEP-01). Moving a database or cache reconnects its container: a brief interruption, data kept | Must |
| NET-07 | Every app, database, cache and load balancer answers to `<name>.<kind>.internal` (for example `orders.database.internal`) inside its network, and reports it as `privateDns`. The v0.3 `internalHost` keeps working | Must |
| NET-08 | Deleting a network that any resource uses is refused with the resources listed (CP-12) | Must |

### Firewalls

| ID | Requirement | Priority |
|---|---|---|
| FW-01 | A firewall names 1–20 targets (apps, databases, caches, load balancers). A resource with at least one firewall accepts only connections a rule allows; one with none accepts everything (v0.3 behaviour) | Must |
| FW-02 | An ingress rule has sources, a protocol (`tcp` default, `udp`, `icmp` or `all`) and ports (single ports or ranges; none means all). A source is `any`, an IPv4 CIDR, a network (`network/backend`) or a resource (`app/web`, `database/orders`, `cache/…`, `loadbalancer/…`) | Must |
| FW-03 | Several firewalls on one resource add up: anything one of them allows is allowed | Must |
| FW-04 | Egress is allowed by default. A firewall with egress default `deny` restricts its targets' outgoing connections to its egress rules (destinations as FW-02). DNS inside the network always works | Should |
| FW-05 | Replies to allowed connections are always allowed. The edge proxy and health checks always reach their app, and a load balancer always reaches its targets' ports, so a firewall never breaks a domain, a deploy or a load balancer | Must |
| FW-06 | A published host port stays reachable through the machine: firewalls govern traffic between resources and networks | Must |
| FW-07 | Rules apply to every container of a target, including each new revision before it takes traffic, and follow resources referenced as sources when their addresses change (a redeploy). Changing a firewall applies within seconds, without restarting anything | Must |
| FW-08 | A firewall's targets and the resources and networks its rules name can't be deleted while it names them (CP-12) | Must |
| FW-09 | Rules removed outside EnclaveStack, or lost when a container restarts, are restored by drift repair within 60 s (CP-10) | Must |

### Replicas and load balancers

| ID | Requirement | Priority |
|---|---|---|
| LB-01 | An app runs 1–10 replicas (`replicas`, default 1). A rolling deploy starts all new replicas, waits until each passes its health check, then switches and stops the old ones (DEP-01). An app that publishes a host port runs exactly one replica | Must |
| LB-02 | A domain spreads requests across all replicas of the serving revision, and the app's internal name answers with every replica | Must |
| LB-03 | A load balancer listens on a port, in the `http` or `tcp` protocol, and forwards to the replicas of 1–8 target apps in one network, each with a port (default the app's port) and a weight (1–256, default 1) | Must |
| LB-04 | Algorithms: `round_robin` (default), `least_conn`, `source` (the same client goes to the same replica) | Must |
| LB-05 | A load balancer checks each replica (`tcp` default, or `http` with a path) and sends nothing to a replica that fails, until it passes again. It reports how many replicas are healthy | Must |
| LB-06 | Replicas added or removed by a deploy are picked up within 2 s without a load balancer restart. Changing a load balancer's targets, weights or algorithm applies without dropping established connections | Must |
| LB-07 | A load balancer is reachable inside its targets' network at `<name>.loadbalancer.internal:<port>`. With `publish`, it also listens on a host port that stays the same across changes. A domain can route to an app; load balancers serve TCP and other ports | Must |
| LB-08 | A target app can't be deleted while a load balancer forwards to it (CP-12) | Must |

### DNS zones and records

| ID | Requirement | Priority |
|---|---|---|
| DNS-01 | A zone names a provider (`cloudflare` or `route53`), the zone's name (`example.com`) and a credentials secret: `{"apiToken"}` for Cloudflare, `{"accessKeyId","secretAccessKey"}` for Route 53. Creating it checks the zone exists and the credentials can edit it, and reports the zone ID and name servers. EnclaveStack never creates or deletes zones at the provider | Must |
| DNS-02 | A record names its zone, a name relative to it (`api`, `@` for the apex, `*.dev` for a wildcard), a type (`A`, `AAAA`, `CNAME`, `TXT`) and either values or `machine: true` (A or AAAA pointing at the machine's public address, `L2-005` DOM-05) | Must |
| DNS-03 | Records are created, changed and deleted at the provider with the resource. Changing the machine's public address updates every `machine: true` record | Must |
| DNS-04 | A record that already exists at the provider with the same values is adopted. One with different values is never overwritten: the record fails with what's there and how to proceed | Must |
| DNS-05 | Drift: a record changed or deleted at the provider is put back within 10 minutes | Should |
| DNS-06 | Provider errors are clear: rejected credentials and missing permissions are permanent failures naming what's missing; rate limits and outages are retried with backoff | Must |
| DNS-07 | Deleting a zone that records or domains use is refused (CP-12), as is deleting its credentials secret | Must |
| DNS-08 | More providers can be added without changing the API: a provider is a plug-in behind one interface | Must |

### Certificates through DNS (DNS-01)

| ID | Requirement | Priority |
|---|---|---|
| CRT-01 | A domain may name a zone whose name the hostname ends with. Its `auto` certificate is then obtained with the ACME DNS-01 challenge through that zone, and the edge proxy doesn't need inbound ports 80/443 to obtain it | Must |
| CRT-02 | A domain in a zone may use a wildcard hostname (`*.example.com`, one leading `*` label). It serves every name below it that no more specific domain serves | Must |
| CRT-03 | Certificates obtained this way renew 30 days before expiry with no user action; a renewal that keeps failing puts the domain in `failed` with the reason before the certificate expires | Must |
| CRT-04 | Challenge records are created and removed by EnclaveStack; nothing is left at the provider after a challenge | Must |
| CRT-05 | Private keys of these certificates are encrypted at rest and never leave the machine except inside the edge proxy | Must |

## 4 · Applies to every new kind

`L2-004` CP-01 – CP-19 apply unchanged to `network`, `firewall`, `loadbalancer`, `zone` and `record`:
- one API, asynchronous phases, idempotent creates, `If-Match`, retry and backoff;
- crash recovery, events, and deletion rules;
- parity across the console, CLI and OpenTofu.

## 5 · Order of delivery

1. **Networks and private DNS.** Firewalls and load balancers live in networks.
2. **Firewalls.**
3. **Replicas and load balancers.**
4. **DNS zones and records.** The provider plug-ins that certificates need.
5. **Certificates through DNS** and wildcard domains.

**Delivery status (2026-10-21):** all five phases are built and tested on every surface; v0.4 is not released yet.

| Phase | Requirements | Built | Where it's recorded |
|---|---|---|---|
| 1 · Networks + private DNS | NET-01 – NET-08 | Yes | ADR-020 |
| 2 · Firewalls | FW-01 – FW-09 | Yes | ADR-021 |
| 3 · Replicas + load balancers | LB-01 – LB-08 | Yes | ADR-022 |
| 4 · DNS zones + records | DNS-01 – DNS-08 | Yes, against local fakes of both providers' APIs; not yet checked against the live Cloudflare or Route 53 | ADR-023 |
| 5 · DNS-01 certificates | CRT-01 – CRT-05 | Yes, against Pebble; not yet checked against the live Let's Encrypt | ADR-024 |

**As built:**
- NET-06: a database or cache that moves is recreated in its new network with the same volume and host port (a brief interruption, data kept), rather than disconnected and reconnected.
- The network output `attached` was dropped; the console counts placed resources itself.

## 6 · Out of scope for v0.4

- IPv6 inside networks, and peering or routing between networks (a resource is in exactly one network).
- Network ACLs per subnet (firewalls apply to resources), deny rules, and rule priorities.
- Autoscaling replicas.
- TLS termination at load balancers (domains terminate TLS); host-based or path-based routing rules.
- Creating or deleting zones at providers; DNSSEC; record types beyond A, AAAA, CNAME and TXT.
- DNS providers other than Cloudflare and Route 53 (the plug-in interface is ready for them).
- Several machines (v0.8).

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-21 | Soupler | v0.4 scope: network, firewall, loadbalancer, zone and record kinds; placement, private DNS, replicas, DNS-01 and wildcard certificates (NET, FW, LB, DNS, CRT requirements) |
| v2 | 2026-10-21 | Soupler | Delivery status: all five phases built and tested on every surface (unreleased); as-built notes |
