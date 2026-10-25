# L2-014 · Public access PRD

---
| Field | Value |
|---|---|
| Document ID | L2-014-public-access-prd-v1 |
| Layer | L2 — Product |
| Status | Draft |
| Version | v1 |
| Created | 2026-10-24 |
| Last Updated | 2026-10-24 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

EnclaveStack runs on a computer at home or in an office. Apps on it can be reached from that computer, and from other machines on the same private network. They can't be reached from the internet, so a team can't open its dev or stage environment from a phone, show it to a client, or test it from another city.

This PRD adds **public access**: a way in from the internet to chosen apps, without opening anything on the router and without exposing the computer. **Which way in is the user's choice**: each is installed from the catalogue's **Public access** category, and any app is then made public through any installed way in. There are two families:

- **App links** give one app an address that the provider hands out:
  - a **quick link** (`*.trycloudflare.com`, random, temporary; no account);
  - **Tailscale** (`<name>.<tailnet>.ts.net`, stable; private to the user's own devices, or public with Funnel; free account);
  - **ngrok** (the account's free `ngrok-free` address, or a reserved one on paid plans);
  - a **custom** connector: any other tunnel service, run from its own image, with no code in EnclaveStack.
- **Domain tunnels** serve the user's own domains (`stage.galiaraa.com`):
  - a **Cloudflare tunnel**, using the user's free Cloudflare account;
  - a **relay** on a small server the user rents, which joins EnclaveStack as an edge machine.

Every public app also gets a **gate**, so strangers can't use dev and stage. The gate is EnclaveStack's own sign-in or a shared password, and optionally Cloudflare Access on top for Cloudflare tunnels.

It came from running a real product (Galiaraa) on EnclaveStack: its dev and stage environments must work from any device while the mini cloud is running.

Builds on:
- `L2-005` domains, the edge proxy and HTTPS (DOM-01 – DOM-09);
- `L2-006` DNS zones and records (DNS-01 – DNS-08) and certificates through DNS (CRT-01 – CRT-05);
- `L2-009` projects and roles (PRJ, IAM);
- `L2-010` machines, joining and networks across machines (MCH, XNET).

Engineering: `L3-002` §16, `L3-003` ADR-051 – ADR-053, `L3-004` DAR-040 – DAR-042, `L3-005` §11, `L3-006` §14, `L3-007` §14. Tests: `L5-001` §2h, `L5-002` §11. User guide: `L5-004` §15.

Product decisions taken with the owner on 2026-10-24:
- **No hosted EnclaveStack service and no DNS server of our own.** The internet-facing edge is Cloudflare's network or the user's own server. EnclaveStack runs the connector at home, writes the routing rules and the DNS records, and nothing else. This keeps the promise in `L1-001`: everything runs on the user's machines, and we operate nothing.
- **The way in is configurable, installed from the catalogue** (owner, 2026-10-24): quick link, Tailscale, Cloudflare, ngrok and the user's own relay are built in, and **custom** runs any other provider's connector. All sit behind one `tunnel` kind, so the gate, the console and the API work the same whichever is used. Vercel and similar hosts were considered and don't fit: their addresses point at code they host, and they can't reach a machine behind NAT.
- **Protection comes with the feature.** EnclaveStack's own gate (sign-in or password) works with every way in. Cloudflare Access is an extra option when the way in is Cloudflare.
- **A quick link for people without a domain**: a random `*.trycloudflare.com` address that changes when it restarts. It is meant for a demo, not for a real environment.
- **Docs and code ship together** on one branch.

## 1 · What problem this solves, in plain words

A visitor's phone finds a website in two steps. It looks up the name in DNS, the internet's phone book, and gets an address. Then it connects to that address. A computer at home has no address of its own that strangers can connect to:
- the home **router** shares one public address between every device in the house, and it drops any connection it didn't ask for (**NAT**);
- many internet providers go further and share one public address between many homes (**CGNAT**), so the router can't even be told to forward a port;
- the home address can change at any time.

All of these block connections coming **in**. None of them blocks connections going **out**. Public access uses this: a small program on the EnclaveStack machine, the **connector**, opens a connection **out** to an **edge** on the internet (Cloudflare's, Tailscale's or ngrok's network, or the user's own server) and keeps it open. The DNS name points visitors at the edge. The edge sends each visitor's request down the connection the machine opened, and EnclaveStack hands it to the right app. Nothing on the router is opened, and the home address is never shown to visitors.

`L5-004` §15 explains this with pictures for users.

## 2 · The user story

> Galiaraa's production runs on AWS. Sunny runs its stage environment on EnclaveStack at home, in the project `galiaraa-stage`. He moves `galiaraa.com`'s DNS to his free Cloudflare account (Cloudflare copies prod's records, which keep pointing at AWS). In that project he adds the zone `galiaraa.com` and a tunnel `home` with his Cloudflare token, then the domain `stage.galiaraa.com` pointing at the `web` app through `home`, with the gate **Sign in**. EnclaveStack creates the tunnel at Cloudflare, starts the connector, writes the one record `stage` and routes it; prod's records are never touched. From his phone on mobile data he opens `https://stage.galiaraa.com`, signs in with his EnclaveStack account, and uses stage. When he later wants dev on EnclaveStack too, it gets its own project and its own zone, `dev.galiaraa.com` (PUB-09). A friend without an account sees only the sign-in page. Later he rents a small server, joins it as an edge machine and moves `stage` to a relay tunnel on it, so no third party sits in front of stage. For a five-minute demo of a new app he makes a quick link and sends it on WhatsApp. His teammates open dev from their phones through **Tailscale**, privately: dev isn't on the internet at all, only on their own devices. When a client needs to see a feature, he switches that link to **public** (Funnel), behind the password gate.

## 3 · New kinds and capabilities

| Kind / capability | What the user gets | Required input | Optional input | What the user reads back |
|---|---|---|---|---|
| `tunnel` (new kind) | A way in from the internet. **App links**: `quick`, `tailscale`, `ngrok`, `custom`. **Domain tunnels**: `cloudflare`, `relay` | name, provider; for app links an app; for `tailscale`, `ngrok` and `cloudflare` a credentials secret; for `relay` an edge machine; for `custom` an image | port, gate; `tailscale` mode and hostname; `ngrok` domain; `custom` command, env and URL | state; for app links the URL; for `cloudflare` the tunnel ID and connections; for `relay` the public address |
| Public domain (domain capability) | A domain served from the internet through a tunnel, with its DNS record written by EnclaveStack | `tunnel` | `zone`, `gate`, `cloudflareAccess` | URL, the DNS record, how the certificate is served, gate |
| Gate (capability of public domains and quick links) | Nobody reaches the app before they sign in, or give the shared password | — | mode `signin` (default), `password` or `none`; password secret; open paths | gate mode, who may pass |
| Public access in the catalogue | One entry per way in, with what it needs, costs and gives; installing it makes its secret and tunnel | the entry's settings | — | the installed tunnel |
| **Make public** (app action) | Make an app public through an installed way in, from the app's page | a way in | gate | the app's public addresses |
| Edge machine (machine capability) | A rented server joined as a machine that runs only the relay | name, `--edge`, public address | — | state, public address, relays it carries |
| Cloudflare Access (domain capability, Cloudflare tunnels only) | Cloudflare checks who the visitor is before the request leaves Cloudflare | emails or email domains | — | the Access application, who it allows |

## 4 · Requirements

### Tunnels

| ID | Requirement | Priority |
|---|---|---|
| TUN-01 | A **tunnel** has a provider, which can't change. **App links**: `quick`, `tailscale`, `ngrok`, `custom`. Each names one app (and optionally a port, default the app's), every request through it goes to that app, and it reports the address the provider gives. **Domain tunnels**: `cloudflare`, `relay`. Each serves the public domains that name it (PUB-*). A tunnel lives in a project like any resource and follows the lifecycle rules of `L2-004` | Must |
| TUN-02 | A **`cloudflare`** tunnel names a credentials secret holding the API token (the token itself, or `{"apiToken"}`, so the zone's secret can serve) and the Cloudflare account ID. On create, EnclaveStack checks the token can manage tunnels in that account, creates a remotely managed tunnel at Cloudflare named `enclave-<project>-<name>`, and starts the connector with the tunnel's run token, which it fetches from Cloudflare each time it makes the connector and never stores. It reports the tunnel ID and how many connections to Cloudflare are open. Deleting it stops the connector and deletes the tunnel at Cloudflare | Must |
| TUN-03 | A **`relay`** tunnel names an **edge machine** (EDG-*). EnclaveStack starts the relay on that machine. It reports the machine's public address, which is where visitors arrive. An edge machine carries one relay tunnel, because the relay takes its ports 80 and 443; another environment needs another edge machine | Must |
| TUN-04 | A **`quick`** link needs no account and no domain. It reports a random `https://<words>.trycloudflare.com` URL. The URL changes whenever it restarts; each change is an event and the new URL is shown at once. The console and CLI say it is temporary, limited to 200 requests at a time, and meant for demos | Must |
| TUN-05 | A tunnel reports whether its connector or relay is connected and answering (`connected`, or `reconnecting` with the reason as an event). After 30 s without a connection EnclaveStack restarts the connector ("No connection for 30 s: restarting the connector."), with backoff if that fails. Every public domain through it shows the same state, with an event | Must |
| TUN-06 | Nothing on the router or the home computer's firewall is opened, for any provider. The connector only makes outgoing connections | Must |
| TUN-07 | A tunnel reaches **only** what is routed through it: a domain tunnel its public domains, an app link its one app. Through a domain tunnel, any other hostname gets a fixed 404 at the edge proxy. The console, the API and the machine itself are never reachable through any tunnel | Must |
| TUN-08 | Deleting a tunnel that public domains use is refused, naming them (CP-12). Deleting its credentials secret is refused while the tunnel exists | Must |
| TUN-09 | Provider errors are clear and classed as in DNS-06. Missing permissions or settings are permanent failures that say what to change ("The token can't edit tunnels: add Account › Cloudflare Tunnel › Edit"; "Turn on HTTPS certificates in your Tailscale admin console, under DNS"). Rate limits and outages are retried with backoff | Must |
| TUN-10 | Changes made at Cloudflare outside EnclaveStack, such as a tunnel's routing rules or a public domain's DNS record, are put back within 10 minutes (drift, as DNS-05). A tunnel deleted at Cloudflare is reported as `failed`, and **Retry** creates it again | Should |
| TUN-11 | Providers are plug-ins behind one interface, so built-in providers can be added later without changing the API (as DNS-08). Anything without a plug-in runs as `custom` (TUN-14) | Must |
| TUN-12 | **`tailscale`**: names a credentials secret `{"authKey"}` (a reusable auth key from the user's Tailscale admin console). The link joins the user's tailnet as a device named `hostname` (default `<project>-<app>`, `-1`… if taken), with the address `https://<hostname>.<tailnet>.ts.net`, which stays the same across restarts. `mode` is **`private`** (Tailscale Serve: only devices in the user's tailnet can open it) or **`public`** (Tailscale Funnel: anyone on the internet). Before connecting, EnclaveStack checks the tailnet has MagicDNS and HTTPS certificates on, and, for `public`, that Funnel is allowed for the device, and says what to switch on. Deleting the link logs the device out, which removes it from the tailnet. The console states Tailscale's limits: Funnel's bandwidth is limited by Tailscale, and it isn't meant for production | Must |
| TUN-13 | **`ngrok`**: names a credentials secret `{"authToken"}` and optionally a `domain` reserved in the ngrok account (paid plans). Without one it uses the account's free dev domain, which ngrok assigns (one per account, so one app per free account). It reports the URL. The console states the free plan's limits: about 1 GB and 20,000 requests a month, and a warning page ngrok shows browsers before the app | Should |
| TUN-14 | **`custom`**: runs any connector image the user names (`image`, `command`, `env`, `secretEnv`), for tunnel services EnclaveStack has no plug-in for. EnclaveStack passes `ENCLAVE_UPSTREAM` (`http://<address>:<port>`), where the connector must send traffic; the gate and the app are behind it. The user types the public `url` the service gives, for display. Its state is the container's: `ready` while it runs. It is hardened like every connector (no capabilities, no other network) | Should |
| TUN-15 | **Public access in the catalogue.** The catalogue (`L2-010` CAT) has a **Public access** category with one entry per way in: Quick link, Tailscale, Cloudflare Tunnel, ngrok, Your own server, Custom. Each says in one line what it gives, what it needs (an account, a domain, a server), what it costs and its limits, with a link to sign up. Installing it asks for its settings (token, app, mode) and creates its credentials secret and tunnel as a stack. It is the same as creating them from **Tunnels** | Must |
| TUN-16 | **Make public.** An app's page has **Make public**: pick an installed way in, or install one (TUN-15). An app link is created for that app; a domain tunnel opens the new public domain form for it. The app's page then lists its public addresses, each with its way in and gate | Must |

### Public domains

| ID | Requirement | Priority |
|---|---|---|
| PUB-01 | A domain may name a **tunnel** whose provider is `cloudflare` or `relay`. It is then a **public domain**: served to the internet through that tunnel, and **not** through the machine's own edge ports | Must |
| PUB-02 | **Through a Cloudflare tunnel**: the domain needs a `zone` with provider `cloudflare` in the tunnel's Cloudflare account. EnclaveStack adds the hostname to the tunnel's routing rules and writes a proxied `CNAME` from the hostname to `<tunnel id>.cfargotunnel.com` in that zone. Cloudflare serves the certificate, so `certificateState` reads `edge` with the issuer "Cloudflare". A zone at another provider is refused with what to do ("Cloudflare tunnels need the domain's DNS at Cloudflare. Move galiaraa.com to Cloudflare, or use a relay tunnel") | Must |
| PUB-03 | **Through a relay tunnel**: the domain's certificate is EnclaveStack's own, `auto` as today (DOM-03, CRT-01). It is obtained through the relay (HTTP-01), or through DNS when the domain names a zone, and its private key never leaves the home machine. With a zone, at any supported provider, EnclaveStack writes an `A` record from the hostname to the edge machine's public address. Without a zone, the domain stays `provisioning` with "Point dev.galiaraa.com's A record to 203.0.113.9" (DOM-05) | Must |
| PUB-04 | The DNS record a public domain writes belongs to that domain. It is adopted when it already has the same value, and never overwrites a different one (DNS-04). It is deleted with the domain or when the domain leaves the tunnel | Must |
| PUB-05 | A wildcard public domain (`*.dev.galiaraa.com`, CRT-02) works through a relay with a zone. Through Cloudflare it is refused unless the zone's plan covers it, with Cloudflare's reason | Should |
| PUB-06 | Changing a domain's tunnel moves it with no more than a few seconds of interruption: the new route and DNS record are made before the old ones are removed. Removing the tunnel from a domain makes it local again | Must |
| PUB-07 | Requests reach the app as through the edge today (DOM-09): the original `Host`, the visitor's IP in `X-Forwarded-For` (the real visitor, not the tunnel), and `https` in `X-Forwarded-Proto` | Must |
| PUB-08 | A domain's page and `get` show: **Public through `<tunnel>`**, the public URL, the DNS record and its state, how the certificate is served, the gate, and, for Cloudflare, the Access rule | Must |
| PUB-09 | **One zone per environment, nothing shared.** A zone belongs to one project, as every resource (`L2-009` PRJ-03, PRJ-04). It is either the whole domain (`galiaraa.com`) or a **subdomain that is a zone of its own** at the provider (`stage.galiaraa.com`, delegated from the parent with NS records). One EnclaveStack environment uses the whole domain's zone. Several environments on one domain each add their own subdomain zone in their own project: Route 53 allows this on any plan; Cloudflare needs its Enterprise plan for a subdomain zone. Creating a zone that doesn't exist at the provider fails with what to do ("stage.galiaraa.com isn't a zone in this Cloudflare account. A subdomain zone needs Cloudflare's Enterprise plan; otherwise add galiaraa.com") | Must |
| PUB-10 | **Other records in a zone are never touched.** A zone may also hold records EnclaveStack didn't make, such as production on AWS. EnclaveStack writes, changes and deletes only the records of its own domains (DNS-04), and the console says so when a zone is added | Must |

### The gate

| ID | Requirement | Priority |
|---|---|---|
| GATE-01 | Every public domain and every app link has a **gate**: `signin`, `password` or `none`. A new one defaults to **`signin`**, so nothing goes public unprotected by accident. The exception is a `private` Tailscale link, which defaults to `none`, because only the user's own tailnet devices can reach it. Domains that aren't public keep no gate, as today | Must |
| GATE-02 | **`signin`**: a visitor without a valid gate session sees an EnclaveStack sign-in page on the same hostname (under `/.enclave/gate/`, a path EnclaveStack reserves), not the app. People who can see the domain's project (any role, `L2-009` IAM) and machine admins may pass, with their EnclaveStack email and password. Others see "This environment is private." | Must |
| GATE-03 | A gate session lasts 12 hours. It is a cookie for that hostname only (`Secure`, `HttpOnly`, `SameSite=Lax`), separate from the console's session, so a gate cookie never opens the console. **Sign out** is at `/.enclave/gate/signout`. Removing a person from the project, or deleting their account, ends their gate sessions within 1 minute | Must |
| GATE-04 | **`password`**: the browser asks for a user name and password (HTTP basic authentication over HTTPS), taken from a secret `{"username","password"}`. It is for sharing with someone who has no account, such as a client. Changing the secret's value changes the password within 1 minute | Must |
| GATE-05 | **`none`**: anyone with the URL reaches the app. The console asks the user to confirm ("Anyone on the internet can open stage.galiaraa.com"), and the domain's page shows **Open to everyone** | Must |
| GATE-06 | **Open paths**: up to 10 path prefixes (for example `/webhooks/`) that skip the gate, so outside services can call the app, such as a payment provider's webhook | Should |
| GATE-07 | Behind a `signin` gate, the app receives `X-Enclave-User` with the visitor's email, and nothing it could mistake for one from a visitor: EnclaveStack removes any `X-Enclave-*` header the visitor sent | Should |
| GATE-08 | Gate sign-ins are limited and recorded like console sign-ins: 10 failed attempts per address per 15 minutes, then a wait; every success and failure is in the audit log with the hostname | Must |
| GATE-09 | **Cloudflare Access** (Cloudflare tunnels only): a public domain may list emails and email domains. EnclaveStack creates a Cloudflare Access application for the hostname that lets only them in (they confirm with a one-time code Cloudflare emails them), and deletes it with the domain. It works together with the EnclaveStack gate: Access is checked at Cloudflare, then the gate at home. The token needs Access permission, and TUN-09 applies | Should |

### Edge machines and relays

| ID | Requirement | Priority |
|---|---|---|
| EDG-01 | An **edge machine** is a server with a public address (a rented VPS, for example) joined as a machine with the role `edge`. It needs Linux with Docker, and inbound TCP 80, TCP 443, TCP 7071 (agent) and UDP 51820 (tunnel) open to the internet | Must |
| EDG-02 | **Joining starts from the main machine**, because an edge machine can reach the internet but often can't reach a home computer. The admin adds the machine with its public address. EnclaveStack prints `enclave agent join --edge --code <code>`; run on the server, it waits for the main machine. The main machine then connects to it, proves it holds the code, and gives it its identity (as MCH-02 – MCH-04, the other way round). The code is single-use and expires after an hour | Must |
| EDG-03 | Nothing is ever placed on an edge machine: apps, databases and every other kind skip it, and it can't be made schedulable. It runs only the mesh (XNET) and the relays of tunnels that name it | Must |
| EDG-04 | A relay passes visitors' connections on ports 80 and 443 to the home machine's edge proxy over the encrypted mesh (WireGuard), **without decrypting them**. The edge machine never holds a certificate's private key, a secret or application data. The edge proxy at home learns the visitor's real address from the relay | Must |
| EDG-05 | The mesh works when the main machine is behind NAT or CGNAT. A machine with no public address dials out to its peers and keeps the connection alive (every 25 s), so the edge machine can reply. Two machines that are both unreachable still can't connect to each other, as before | Must |
| EDG-06 | When an edge machine goes offline, its relay tunnels show `reconnecting` and every public domain on them says so. The machine-offline alert fires (`L2-009`, ALR). When it returns, the relays resume with no action | Must |

## 5 · On each surface

| ID | Surface | Requirement | Priority |
|---|---|---|---|
| CON-76 | Console | A **Tunnels** section in the Networking group: a list with provider, state and what each serves; a create form that changes with the provider (credentials with the permissions or settings needed; edge machine; app and port; Tailscale mode and name; ngrok domain; custom image, command, environment and URL); a tunnel's page with its state, connections or public address, its public domains, and an app link's URL with Copy ("Temporary" for a quick link) | Must |
| CON-77 | Console | The domain form has **Public access**: "Only on this machine" (default) or "From the internet through a tunnel", which shows the tunnel, the zone, the **Gate** (Sign in, Password and its secret, or Open to everyone, which asks to confirm), open paths, and, for Cloudflare tunnels, **Cloudflare Access** emails. A public domain's page shows the public URL, DNS record, certificate, gate and Access | Must |
| CON-78 | Console | Machines has **Add an edge machine**: name and public address, then the join command with Copy and the ports to open, and the machine's state while it waits. An edge machine's page shows the relays it carries and no placement actions | Must |
| CON-79 | Console | The gate's sign-in page, password prompt and "This environment is private" page follow `L2.5-002` §21 and EnclaveStack's look, naming the hostname | Must |
| CON-80 | Console | The catalogue's **Public access** category with its six entries (TUN-15), and **Make public** on an app's page with the app's public addresses (TUN-16) | Must |
| CLI-56 | CLI | `enclave tunnel create NAME` with `--provider quick --app A`; `--provider tailscale --credentials S --app A [--mode private\|public] [--hostname H]`; `--provider ngrok --credentials S --app A [--domain D]`; `--provider custom --app A --image I [--command C]… [--env K=V]… [--secret-env K=SECRET]… [--url U]`; `--provider cloudflare --credentials S --account-id ID`; `--provider relay --machine M`; app links take `[--port P] [--gate signin\|password\|none] [--gate-password-secret S] [--gate-open-path P]…` (`none` asks to confirm unless `--yes`); `tunnel update NAME` (gate, port, Tailscale mode, custom settings, credentials), `tunnel ls` (with each address), `get` (with the URL), `delete`. `catalogue ls` shows each entry's category, `catalogue show` the sign-up page; an entry installs as a stack (`stack create NAME --template quick-link --param app=A`) or as the tunnel it describes | Must |
| CLI-57 | CLI | `domain create` and `domain update` take `--tunnel T` (`""` makes it local again), `--gate signin\|password\|none` (with `none` it asks to confirm unless `--yes`), `--gate-password-secret S`, `--gate-open-path P` (repeatable) and `--access-email E` / `--access-email-domain D` (repeatable) | Must |
| CLI-58 | CLI | `enclave machine add NAME --edge --address A [--agent-port P]` prints the join command and the ports to open; `enclave agent join --edge --code C [--listen :7071]` waits for the main machine | Must |
| TF-51 | OpenTofu | `enclave_tunnel` with `tunnel_provider` (`provider` is a reserved word in OpenTofu), `credentials_secret`, `account_id`, `machine`, `app`, `port`, `gate = { mode, password_secret, open_paths }`, `mode`, `hostname`, `domain`, `image`, `command`, `env`, `secret_env`, `connector_url` (a custom connector's address); computed `url`, `tunnel_id`, `public_address`, `gate_mode` (the gate in force) | Must |
| TF-52 | OpenTofu | `enclave_domain` takes `tunnel`, `gate = { mode, password_secret, open_paths }` and `cloudflare_access = { emails, email_domains }`; computed `public` and `dns_record = { type, name, value, state }` | Must |
| TF-53 | OpenTofu | `enclave_machine` takes `role = "edge"`, `address` and `agent_port`; `schedulable` is `false` for an edge machine; computed `join_command` (sensitive) | Must |

## 6 · Acceptance

- **Quick link:** with no account, a quick tunnel to an app gives a `trycloudflare.com` URL. A phone on mobile data opens it, meets the sign-in page, signs in, and sees the app. After a restart the URL changes and the console shows the new one.
- **Tailscale:** with a free Tailscale account, a `private` link opens on a phone in the tailnet and not on one outside it; switched to `public`, it opens anywhere, behind the gate. Its address stays the same across restarts, and deleting it removes the device from the tailnet.
- **ngrok and custom:** an ngrok link with a free token serves the app at the account's dev address. A custom connector (for example `cloudflared` run by hand as an image) serves the app at the URL it prints.
- **Catalogue:** each Public access entry installs from the catalogue, and **Make public** on an app uses it.
- **Cloudflare:** with a real Cloudflare account and a zone, a tunnel and two public domains with `signin` gates work from a phone on mobile data. The DNS records and routing rules appear at Cloudflare, and are gone after the domains and tunnel are deleted. Nothing on the router was changed.
- **Relay:** with a real VPS joined as an edge machine while the main machine sits behind NAT, a relay tunnel and a public domain with a zone at Route 53 serve HTTPS with a Let's Encrypt certificate obtained through the relay. The VPS holds no private key. The app sees the visitor's real IP.
- **Gate:** a person without access to the project can't pass `signin`; the right password passes `password`; an open path skips the gate; a gate cookie doesn't open the console; removing a person ends their gate session within a minute; ten failed attempts slow the address down.
- **Isolation:** through any tunnel, an unknown hostname gets the fixed 404, and neither the console nor the API answers.
- **Failure:** stopping the connector shows the tunnel and its domains `reconnecting`, and they recover by themselves. Turning off the edge machine fires the offline alert.
- **Surfaces:** the console, CLI and OpenTofu each create, change, show and remove tunnels, public domains with every gate mode, and edge machines.

## 7 · Order of delivery

1. **The tunnel kind, quick links, custom connectors, the gate, the catalogue category and Make public.** These prove the path from the internet to an app and its protection, with no account needed.
2. **Tailscale**, private and public.
3. **Cloudflare tunnels and public domains**, with DNS records and Cloudflare Access.
4. **Edge machines and relays**: reverse join, the mesh behind NAT, and the relay.
5. **ngrok.**

Each way in gets its catalogue entry in the step that builds it.

Each step ships on every surface with its tests before the next starts.

## 8 · Out of scope

- An EnclaveStack-hosted edge or shared domain (such as `*.enclavestack.dev`), and running a DNS server.
- Built-in plug-ins beyond these five (`custom` covers the rest), and ngrok's own domains as domain tunnels.
- Exposing TCP or UDP services such as databases or SSH through a tunnel. Public access is HTTP and HTTPS to apps. A load balancer can be put behind a domain.
- Single sign-on (Google, GitHub, OIDC) in the gate. Cloudflare Access covers outside identities for Cloudflare tunnels.
- Production hosting guarantees: public access is only up while the machine and the way in are up. Production belongs on a machine that is always on.
- Several edge machines serving one domain for redundancy.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-24 | Soupler | Public access: ways in installed from the catalogue (quick, Tailscale, ngrok, custom, Cloudflare, relay), public domains, the gate, edge machines and relays (TUN-01 – TUN-16, PUB-01 – PUB-10, GATE-01 – GATE-09, EDG-01 – EDG-06); console CON-76 – CON-80; CLI-56 – CLI-58; TF-51 – TF-53 |
