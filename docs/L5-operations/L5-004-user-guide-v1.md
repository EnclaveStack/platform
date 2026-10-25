# L5-004 · User Guide: install, run and use EnclaveStack

---
| Field | Value |
|---|---|
| Document ID | L5-004-user-guide-v1 |
| Layer | L5 — Operations |
| Status | Draft |
| Version | v6 |
| Created | 2026-10-23 |
| Last Updated | 2026-10-25 |
| Owner | Soupler |
| Reviewers | Soupler |
| Copyright | © 2026 Soupler |
| License | Apache-2.0 |
---

## Purpose

One guide, start to finish, for a person who has never seen EnclaveStack: what it is, how to install and start it, how to use it from the web console, the `enclave` command and OpenTofu, how to connect your own code to what you create, how to keep it running, and how to remove it. Every command here is one that works today; each one says what it does and how often you run it.

`README.md` is the short version, the website's guides follow this one, and `L4-001` is for people who change EnclaveStack itself. Where this guide and the code disagree, the code is right and this guide has a bug: fix the guide.

## 1 · What you are setting up

EnclaveStack is **one program** that turns a Mac or a Linux computer into a small cloud of your own. You tell it what you want (a PostgreSQL database, a Redis cache, an app from a container image, an S3-compatible bucket, an encrypted secret, a web address with HTTPS) and it creates it, as containers, on the Docker that is already on that computer. It then keeps those things running, restarts what fails and shows you their state.

There are **three ways to tell it what you want**, and all three talk to the same server, so what you make one way shows up in the others:

| Way | What it is | Good for |
|---|---|---|
| **The console** | A web page the program serves (`http://127.0.0.1:7070`) | Looking around, clicking, seeing state and logs, copying connection details |
| **The `enclave` command** | The same program, used from a terminal | Quick one-off changes, scripts, anything you would otherwise click twice |
| **OpenTofu** | Text files that describe what you want; OpenTofu makes EnclaveStack match them | Setups you want to repeat, review in Git and change safely |

There is no installer yet. You **build the program from its source code** once, then run it. "Build" here only means turning the source into the one program; it does not install anything system-wide.

What runs where:

- **The server** (`enclave server`) is the program running. It serves the console and the API on port 7070, keeps its database and keys in a folder (`~/.enclave`), and talks to Docker to create and watch containers.
- **Docker** (Docker Desktop on a Mac, Docker Engine on Linux) is what actually runs your databases and apps. EnclaveStack does not replace Docker; it drives it.
- **Your containers** are named `es-<kind>-<name>` (for example `es-database-orders`). They keep running if you stop the server, and EnclaveStack picks them up again when you start it.

## 2 · Before you start

### 2.1 What you need

| Need | Version | Why | Check |
|---|---|---|---|
| A Mac or a Linux computer | macOS or Linux, 64-bit | Windows is not supported | |
| **Docker**, running | Docker Desktop, or Docker Engine 23 or later | Runs your databases and apps. Not needed for the sample-data demo | `docker info` prints server details, not an error |
| **Go** | 1.26 or later | Builds the program | `go version` |
| **Node.js** | 22.12 or later | Builds the web console | `node -v` |
| **pnpm** | 9 | Installs the console's libraries | `pnpm -v` |
| **git** and **make** | any recent | Downloads the code and runs the commands below | `git --version`, `make --version` |
| **curl** | any | Used by `make demo` and `make tofu` to check the server is up | `curl --version` |
| **OpenTofu** | optional, current release | Only for the OpenTofu way of using it | `tofu version` |
| **QEMU** | optional, 8 or later | Only if you want VMs | `qemu-system-aarch64 --version` (or `qemu-system-x86_64`) |

Disk and memory: the build needs a few hundred MB, and each container you create uses what its image needs. On a Mac, Docker runs in a virtual machine, so what your containers may use is limited by Docker Desktop's memory setting, not by the Mac's total.

### 2.2 Installing the tools

**macOS** (with [Homebrew](https://brew.sh)):

```bash
brew install go node pnpm git make        # the build tools
brew install --cask docker                # Docker Desktop; open it once and wait until it says it is running
brew install opentofu                     # optional: only for the OpenTofu way
brew install qemu                         # optional: only for VMs
```

**Linux** (Debian or Ubuntu shown; use your distribution's equivalents):

```bash
sudo apt install git make curl
# Docker Engine: follow https://docs.docker.com/engine/install/ and add yourself to the docker group, then log in again
# Go 1.26+, Node 22.12+: from https://go.dev/dl/ and https://nodejs.org (or your version manager)
corepack enable && corepack prepare pnpm@9 --activate
# optional: OpenTofu from https://opentofu.org/docs/intro/install/ ; VMs: sudo apt install qemu-system qemu-utils
```

Check each tool with the commands in the table before going on. A tool that is missing or too old is the most common cause of a failed first build.

## 3 · Get the code (once)

```bash
git clone https://github.com/EnclaveStack/platform.git enclavestack
cd enclavestack
pnpm install
```

- `git clone` downloads the source into a folder called `enclavestack`.
- `cd enclavestack` moves your terminal into it. **Every `make` command in this guide is run from this folder.**
- `pnpm install` downloads the JavaScript libraries the web console is built from. It is slow the first time (a minute or two) and you run it once per download. You run it again only after updating the code, if the console's libraries changed.

## 4 · Two ways to start

| I want to… | Use | Needs Docker |
|---|---|---|
| Look around with sample data, creating nothing real | `make demo` (§4.1) | No |
| Run real databases and apps on this computer | `make start` (§4.2) | Yes, running |

### 4.1 Try it with sample data (`make demo`)

```bash
make demo
```

What it does, in order (the script is `examples/demo/run.sh`):

1. **Builds** the console and the program (`make build`).
2. **Starts a server** on a *simulated* engine, so it needs no Docker and creates no containers.
3. **Creates an administrator** (`admin@enclave.local`, password `enclave-demo`) and an API token for the terminal.
4. **Fills it with sample data**: a secret, a PostgreSQL database, a Redis cache, a bucket, an app, a daily job, a WordPress site from the catalogue and a second project.
5. Prints the console address, the sign-in and an `export` line for the terminal, then keeps running.

Open **http://127.0.0.1:7070** and sign in with the email and password above. Press **Ctrl-C** in the terminal to stop it; with the simulated engine, everything made by the demo goes with it.

Options (put them after `make demo`):

| Option | Effect |
|---|---|
| `DRIVER=docker` | The sample data becomes real containers. The data is kept in `~/.enclave-demo`, and a second run picks it up |
| `PORT=7171` | Use another port if 7070 is taken |
| `DATA=/tmp/my-demo` | Keep the data in that folder between runs |

The demo is for looking. Do not use it for real work: its password is public, and the simulated engine proves nothing about whether a real database starts.

### 4.2 Run it for real (`make start`)

Make sure **Docker is running** (`docker info` works), then:

```bash
make start
```

What it does:

1. **Builds** (`make build`): the web console first, then the single `enclave` program with the console inside it, saved as `./build/enclave`. It also builds the OpenTofu plug-in (`./build/terraform-provider-enclave`). The first time takes a few minutes; later runs are faster.
2. **Runs** `./build/enclave server`. This is EnclaveStack itself. It uses the real Docker engine and keeps its data in `~/.enclave`.

What you will see: build output, then a few log lines, then this block (your version and code will differ):

```
EnclaveStack 6364bf0
Console: http://127.0.0.1:7070

First run — open the console and enter this setup code:

    HUTS-WSBW-XWBP

It works once, and a restart replaces it until setup is done.
```

After setup is done, the code is not printed again; the server just logs that the control plane started.

**Leave this terminal open.** The server runs in it and stops when you close it or press Ctrl-C. Containers you already created keep running; EnclaveStack picks them up the next time you start it.

To pass options to the server, use `ARGS`:

```bash
make start ARGS='--listen 127.0.0.1:7171'      # another port
make start ARGS='--log-level debug'            # more detail in the log
```

`./build/enclave server --help` lists every option. The ones you are most likely to want:

| Option | Default | Meaning |
|---|---|---|
| `--listen` | `127.0.0.1:7070` | Where the console and API listen. Anything other than `127.0.0.1` needs TLS in front of it |
| `--data-dir` | `~/.enclave` | Where the database and the master key live |
| `--driver` | `docker` | `docker` for real containers; `fake` for the simulated engine |
| `--publish-address` | `127.0.0.1` | The address published ports bind to. Use `0.0.0.0` to reach apps from other devices |
| `--console-url` | none | The address people open the console at, for links in alerts and invitations |
| `--edge-address` | `127.0.0.1` | The address the web-address (domain) proxy binds to; `0.0.0.0` serves domains publicly |

### 4.3 Ports used

| Port | Used by | When |
|---|---|---|
| 7070 | The console and API | Always |
| A high port chosen for you (for example 41000 or 50019) | Ports of apps, databases and caches you publish, on `127.0.0.1`. The resource's page and `enclave get` show it | When you create them |
| 80 and 443 | The edge proxy that serves domains | Only after you create the first domain |
| 7071/tcp and 51820/udp | Talking to other computers you add | Only when you add a machine |

If something else already uses 7070, the server says `address already in use`: stop the other one or pick another port with `--listen`.

## 5 · First-run setup (once)

The first time the server starts with an empty `~/.enclave`, it requires a one-time **setup code** (printed in the terminal) so that nobody who can merely reach the port can claim your server.

1. Open **http://127.0.0.1:7070** in a browser. It shows the first-run setup page.
2. **Setup code:** copy it from the terminal.
3. **Machine name:** starts as your computer's own name. This is the name people see (for example in the console's header). You can change it later from the machine's page.
4. **Admin email and password:** choose them. The password needs at least 12 characters. This account is the administrator.
5. Click **Create admin and open console**. You land on the **Overview**, which is empty.

If you lose the code, restart the server: until the administrator exists, every start prints a fresh code and the old one stops working.

## 6 · Using the console

The console is the page you just opened. Its left side lists everything EnclaveStack can make, grouped: Compute (apps, jobs, VMs, load balancers), Data (databases, caches, buckets, volumes, snapshots, backups, search), Messaging and media (queues, topics, email, transcoders), Security (secrets, secret stores), Install (the catalogue and stacks), Operate (machines, monitoring, alerts, channels, log exports) and Settings. Each section has a **Guide** button that explains the service in plain words.

**Make something.** Open a section (for example Databases) and click **New database**. Fill in the name and any options; every field has a hint. Click **Create**. A progress strip shows the steps; when it says **Ready**, the thing exists.

**See how to use it.** A ready resource's page shows its connection details: the host and port to use from your own computer, the host and port to use from another EnclaveStack app (§9), the user, and the name of the secret that holds the password. It also has tabs for logs, events and (for most kinds) metrics, and buttons such as Restart and Delete.

**Projects.** The menu at the top of the left side chooses the project you are working in. A project is a separate set of names: you can have a database called `orders` in two projects. "All projects" shows everything.

**Find anything.** Press ⌘K (Mac) or Ctrl+K and type a name.

**API tokens.** Settings → API tokens makes tokens for scripts, CI and OpenTofu (§8). A token is shown once when you create it.

## 7 · Using the terminal: the `enclave` command

### 7.1 Make the command available (once)

`make start` builds the program as `./build/enclave`, inside the repository folder. Your terminal does not know the word `enclave` until you do one of these:

```bash
make install                      # recommended: copies enclave to a folder on your PATH
export PATH="$PWD/build:$PATH"    # this terminal only: lasts until you close it
./build/enclave …                 # no set-up: type the path every time, from the repository folder
```

`make install` builds, then copies the program to the first of `/opt/homebrew/bin` or `/usr/local/bin` that exists and you can write to. Use `make install BINDIR=$HOME/bin` to choose the folder (and add it to your PATH). If neither folder is writable it says so and prints what to do. Open a new terminal, or run `hash -r` in an open one, then check:

```bash
enclave version
```

Run `make install` again after you update the code, so the command matches the server.

### 7.2 Sign in (once per computer)

```bash
enclave login
```

It asks for your email and password (the administrator account from §5), signs in to the server at `http://127.0.0.1:7070` and saves a token in `~/.config/enclave/config.json`. You are not asked again. To use another server: `enclave login --server https://enclave.example.com`.

```bash
enclave whoami        # who you are signed in as, and on which server
enclave logout        # revoke this computer's token and forget it
enclave password      # change your password
```

### 7.3 The shape of every command

```
enclave <kind> <action> <name> [options]
```

- **kind**: what you are making: `app`, `db`, `cache`, `bucket`, `secret`, `job`, `domain`, `stack`, `queue`, `topic`, `vm`, `volume` and more. `enclave --help` lists them all.
- **action**: `create`, `list`, `get`, `update`, `delete`, `restart` (the exact actions depend on the kind; `enclave db --help` lists them).
- **options**: `--wait`, `--help` and the kind's own options.

Options that work on every command:

| Option | Meaning |
|---|---|
| `--wait` | On create, update and delete: do not return until the thing is ready (or gone). Without it, the command returns at once and the work continues in the background |
| `--timeout 20m` | How long `--wait` waits (default 10 minutes) |
| `-p shop` / `--project shop` | Work in another project (or set `ENCLAVE_PROJECT`, or run `enclave project use shop` to make it the default) |
| `-o json` | Print machine-readable JSON, for scripts |
| `--server`, `--token` | Talk to a different server or use a different token (or set `ENCLAVE_SERVER`, `ENCLAVE_TOKEN`) |
| `--help` | Show every option of that command |

### 7.4 Making things

```bash
# A PostgreSQL database. Engines: postgres (default), mysql, mongodb, clickhouse. Engine and version cannot change later.
enclave db create orders --wait
enclave db create reports --engine mysql --version 8.4 --wait

# A Redis cache with a memory limit in MiB (64–4096)
enclave cache create sessions --max-memory 128 --wait

# An S3-compatible bucket
enclave bucket create uploads --versioning --wait

# A secret: read the value from standard input so it stays out of your shell history
printf 'sk_live_example' | enclave secret create stripe-key --from-stdin

# An app from a container image
enclave app create web --image nginx:1.27-alpine --port 80 --publish --wait

# …with environment variables, a secret, a memory limit and a health check
enclave app create api --image ghcr.io/acme/api:1.4 --port 8080 --publish \
  --env LOG_LEVEL=info --secret-env STRIPE_KEY=stripe-key --memory 512 --health http:/healthz --wait

# …or an app built from a Git repository that has a Dockerfile
enclave app create site --git https://github.com/acme/site.git --ref main --port 3000 --publish --wait

# A scheduled job
enclave job create nightly --image alpine:3 --schedule "0 2 * * *" --wait -- echo hello

# A web address with automatic HTTPS in front of an app (*.localhost works with no DNS)
enclave domain create web-domain --app web --hostname web.localhost --wait

# A backup of a database to a bucket, every night at 03:00, keeping the last 7
enclave backup create orders-nightly --source database/orders --bucket uploads --wait

# A ready-made product from the catalogue: WordPress, Ghost, n8n, Uptime Kuma, Gitea, Plausible
enclave catalogue ls
enclave catalogue show wordpress
enclave stack create blog --template wordpress --param domain=blog.localhost --wait
```

`--publish` makes an app's port reachable from your computer (on `127.0.0.1` and a random high port, shown in the app's details). Without it, the app is reachable only by other things inside EnclaveStack.

### 7.5 Looking at what you have

```bash
enclave ls                         # everything in the current project, of every kind
enclave ls -A                      # every project you can see, with a PROJECT column
enclave ls --kind database         # only one kind
enclave db list                    # the same, through the kind
enclave db get orders              # show one: host, port, user, password secret and a ready-made Connect line (-o json for everything)
enclave get app web                # show one by kind and name
enclave logs app web --tail 100    # the last lines of its output
enclave logs app site --build      # the build log of an app built from Git
enclave events app web             # its history: created, ready, restarted, failed…
enclave metrics app/web            # CPU, memory and more (KIND/NAME; or the word machine for this computer's)
enclave secret get stripe-key --reveal    # print a secret's value (audited)
```

### 7.6 Changing and removing

```bash
enclave app update web --image nginx:1.28-alpine --wait     # change an app: only the flags you pass change (--env and --secret-env replace the whole set)
enclave app redeploy web --wait    # pull the image again (or build the latest commit) and roll out a new revision
enclave app restart web            # restart it with the same version
enclave db restart orders
enclave retry app web --wait       # try again after a failure
enclave delete app web --wait      # asks first; add -y to skip the question. Data is lost
enclave db delete orders
```

### 7.7 Projects and tokens

```bash
enclave project create shop --description "The shop"
enclave project use shop                  # work in it from now on
enclave app create storefront --project shop --image nginx:1.27-alpine --port 80    # or one command in another project

enclave token create --name ci --role editor                  # a token for a script (shown once)
enclave token create --name deploy --role editor --project shop --expires-in-days 30
enclave token list
enclave token revoke <id>
```

Give a token the narrowest role it needs: `viewer` can only read, `editor` can create and change resources, `admin` can also manage projects and people.

### 7.8 In a script

```bash
export ENCLAVE_SERVER=http://127.0.0.1:7070
export ENCLAVE_TOKEN=$(enclave token create --name my-script --role editor)
enclave db create orders --wait -o json 2>/dev/null | jq -r '.outputs.internalHost'
```

Progress messages such as "Creating database orders…" go to standard error, so `2>/dev/null` keeps only the JSON on standard output. Commands return a non-zero exit code on failure, so `set -e` in a script stops at the first problem.

### 7.9 Shell completion (optional)

`enclave completion zsh` (or `bash`, `fish`) prints a completion script. For zsh: `enclave completion zsh > "${fpath[1]}/_enclave"`, then open a new terminal.

## 8 · Using OpenTofu

### 8.1 What it is and why you would use it

OpenTofu (an open-source fork of Terraform) reads **text files** that describe what you want, compares them with what exists, shows you the difference and applies it. For EnclaveStack that means you write "a database called orders and an app that uses it" in a file, and OpenTofu makes the server match. The advantages over typing commands:

- **Repeatable.** The same files make the same setup, on your computer or a friend's.
- **Reviewable.** The files live in Git, so a change is a diff someone can read before it is applied.
- **Safe to change.** `plan` shows exactly what would change before anything does. Remove a block and that resource is deleted; change a value and only that is updated.
- **Connected.** One resource can read another's values (an app gets the database's address), and OpenTofu creates them in the right order.

### 8.2 What `make tofu` does for you

Doing it by hand takes six steps (build the plug-in, tell OpenTofu where it is, sign in, make a token, export it, run `tofu`). `make tofu` does them for you each time. The script is `examples/opentofu/run.sh`; in order, it:

1. Checks `tofu` is installed and the server answers (if not, it tells you to run `make start`).
2. **Builds the plug-in** (`./build/terraform-provider-enclave`): the part that teaches OpenTofu what an `enclave_database` is.
3. **Writes `~/.tofurc.enclave`**, a small file that tells OpenTofu to use that plug-in. Your own OpenTofu configuration is not touched.
4. **Makes sure it has a working token.** It reuses the one saved in `~/.enclave-opentofu-token`; if there is none, it runs `enclave login` (asks once) and creates a token named `opentofu`.
5. **Runs `tofu <CMD>`** in a folder, with the server address and token set for that one run.
6. After a successful `destroy` that leaves nothing managed, **deletes the empty state files** OpenTofu leaves behind (§8.6).

```bash
make tofu CMD=plan                          # show what would be created; creates nothing
make tofu                                   # create it (CMD=apply is the default); type yes when asked
make tofu CMD=destroy                       # remove it again
make tofu DIR=path/to/your/files CMD=plan   # the same for your own folder
make tofu ARGS=-auto-approve                # skip the "type yes" question (for scripts)
make tofu CMD=output                        # print the values the files declare as outputs
```

It needs EnclaveStack running (`make start` in another terminal) and `tofu` installed.

### 8.3 Try the included example

`examples/opentofu/main.tf` makes a database, a cache, a secret and an app that uses all three. It is a good first run:

```bash
make tofu CMD=plan        # ends with: Plan: 4 to add, 0 to change, 0 to destroy.
make tofu                 # type yes; takes about 30 seconds the first time (images are pulled)
```

When it finishes it prints outputs such as `app_url = "http://127.0.0.1:50065/"`. Open that address, or run `curl` on it: it answers `hello from enclave (db at es-database-orders)`, which shows the app started, received its secret and found the database by name. The four resources also appear in the console. Remove them with `make tofu CMD=destroy`.

### 8.4 Writing your own files

Make a folder with a file called `main.tf`:

```hcl
terraform {
  required_providers {
    enclave = { source = "enclavestack/enclave" }
  }
}

# The server and token come from ENCLAVE_SERVER and ENCLAVE_TOKEN, which `make tofu` sets.
# You can also write server = "…", token = "…" and project = "…" here.
provider "enclave" {}

variable "greeting" {
  type      = string
  sensitive = true          # keeps the value out of the plan output
  default   = "hello"
}

resource "enclave_secret" "greeting" {
  name  = "greeting"
  value = var.greeting
}

resource "enclave_database" "orders" {
  name    = "orders"
  version = "16"
}

resource "enclave_app" "web" {
  name    = "web"
  image   = "nginx:1.27-alpine"
  port    = 80
  publish = true
  env        = { DB_HOST = enclave_database.orders.internal_host }     # one resource feeding another
  secret_env = { GREETING = enclave_secret.greeting.name }             # a secret, by name
}

output "app_url" {
  value = "http://${enclave_app.web.host}:${enclave_app.web.host_port}/"
}
```

Run it with `make tofu DIR=<that folder> CMD=plan`, read the plan, then `make tofu DIR=<that folder>`.

How to read it:

- **`resource "enclave_database" "orders"`**: "make a database". `enclave_database` is the kind; `"orders"` is a label only OpenTofu uses; `name = "orders"` is the name in EnclaveStack.
- **`enclave_database.orders.internal_host`**: a value that does not exist until the database is created. OpenTofu waits for it, which is also how it knows to create the database before the app.
- **`variable`** and **`sensitive`**: inputs you can change without editing the resource, with passwords kept out of the output. Set one with `make tofu ARGS='-var greeting=hi'` or a `terraform.tfvars` file.
- **`output`**: a value printed at the end and available to `make tofu CMD=output`.

Each EnclaveStack kind has a matching resource: `enclave_app`, `enclave_database`, `enclave_cache`, `enclave_bucket`, `enclave_secret`, `enclave_domain`, `enclave_job`, `enclave_backup`, `enclave_volume`, `enclave_vm`, `enclave_queue`, `enclave_topic`, `enclave_email`, `enclave_network`, `enclave_firewall`, `enclave_load_balancer`, `enclave_zone`, `enclave_record`, `enclave_cdn`, `enclave_search`, `enclave_transcoder`, `enclave_snapshot`, `enclave_alert`, `enclave_channel`, `enclave_dashboard`, `enclave_log_export`, `enclave_secret_store`, `enclave_stack`, `enclave_project`, `enclave_user` and `enclave_machine`. Every service page on the website shows its OpenTofu block and every setting it takes; `tofu providers schema -json` lists them too.

### 8.5 The lifecycle

| You run | What happens |
|---|---|
| `plan` | OpenTofu reads your files and what exists, and prints the difference. **Nothing changes.** Always read it |
| `apply` | Makes the difference real, after you type `yes`. It waits until each resource is ready |
| Edit a file, then `plan` / `apply` | Only what you changed is updated. A value that cannot change in place (a database's engine) is shown as "must be replaced" in the plan |
| Delete a block, then `apply` | That resource is removed from EnclaveStack, and its data is lost |
| `destroy` | Removes everything the files describe |

### 8.6 State files: what they are

Next to your files OpenTofu keeps `terraform.tfstate`: its **memory** of what it created, so it can tell what changed. It is not EnclaveStack's copy of your resources; EnclaveStack holds those. Rules:

- **Keep it** while you use the files. If you delete it, OpenTofu forgets what it made and will try to create everything again, which fails because the names are taken.
- **Never commit it.** It can contain passwords and secrets. `.gitignore` already excludes `*.tfstate` in this repository; add the same line in your own.
- `terraform.tfstate.backup` is a copy from just before the last change. It is safe to delete.
- After a clean `make tofu CMD=destroy`, the state is empty and `make tofu` deletes both files for you.

### 8.7 Doing it by hand (without `make tofu`)

```bash
make provider-dev                                    # builds the plug-in and writes ~/.tofurc.enclave
export TF_CLI_CONFIG_FILE=$HOME/.tofurc.enclave      # tells OpenTofu to use that file
export ENCLAVE_SERVER=http://127.0.0.1:7070
export ENCLAVE_TOKEN=$(enclave token create --name opentofu)
cd examples/opentofu && tofu plan
```

OpenTofu prints a warning that "provider development overrides are in effect". That is expected: the plug-in is the one you built, not one downloaded from a registry, so `tofu init` is not needed.

### 8.8 In CI

Create a token with the `editor` role (`enclave token create --name ci --role editor --expires-in-days 90`), store it as a secret in your CI, and set `ENCLAVE_SERVER` and `ENCLAVE_TOKEN`. The server must be reachable from CI, which means it is behind your own TLS (§4.2, `--console-url`).

## 9 · Connecting your own code to what you created

Everything you make has **two addresses**:

| Address | Looks like | Use it from |
|---|---|---|
| **Published (host)** | `127.0.0.1:50019` | Your own computer: a script, a database client, your code running outside EnclaveStack. The port is random and fixed for that resource |
| **Internal** | `es-database-orders:5432` | Another app **inside** EnclaveStack. Always use this between your apps; it works on any machine and survives restarts |

**A database.** Open its page, or run `enclave db get orders`. It shows the host and port, the user, the database name, the name of the **secret** that holds the password (`orders-password`) and a ready-made line such as `Connect  postgres://orders:<password>@127.0.0.1:41000/orders`. Read the password with `enclave secret get orders-password --reveal` and put it where `<password>` is.

**Giving an app its connection details.** Do not paste passwords into code. Pass them as environment variables:

```bash
enclave app create api --image acme/api:1 --port 8080 --publish \
  --env DB_HOST=es-database-orders --env DB_PORT=5432 --env DB_USER=orders --env DB_NAME=orders \
  --secret-env DB_PASSWORD=orders-password --wait
```

`--env` sets a plain value; `--secret-env KEY=SECRET_NAME` sets `KEY` to the secret's value when the container starts, without the value ever appearing in commands, logs or the console. In OpenTofu the same is `env = { … }` and `secret_env = { … }` (§8.4).

**A bucket.** Its page shows an S3 endpoint. Any S3 client or SDK works with that endpoint. There are two ways to give an app keys for it; prefer the first.

- **Keys of its own (bucket access).** Give the app (or a job) bucket access: each bucket it needs, and whether it only **reads** it or **reads and writes**. EnclaveStack makes the app an object store user of its own that reaches exactly those buckets at those levels and nothing else, and gives its containers the keys in the standard AWS variables, so S3 code works unchanged:

  | Variable | Value |
  |---|---|
  | `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY` | the app's own keys |
  | `AWS_REGION` | `us-east-1` |
  | `AWS_ENDPOINT_URL_S3`, `AWS_ENDPOINT_URL` | `http://es-system-objectstore:9000` |

  Use **path-style** addresses in your S3 client (`forcePathStyle: true` in the AWS SDK for JavaScript, `addressing_style = path` in boto3; the AWS CLI does this by itself with a custom endpoint).

  ```bash
  enclave app create uploader --image acme/uploader:1 --port 8080 --bucket media:readwrite --wait
  enclave app create storefront --image acme/shop:1 --port 80 --bucket media:read --wait
  enclave job create billing --schedule "0 1 * * *" --image acme/billing:1 --bucket invoices:readwrite --wait
  enclave app update storefront --bucket media:read --bucket invoices:read   # the list replaces the old one
  enclave app update storefront --bucket ""                                 # take all access away
  ```

  In the console it is **Bucket access** in the app and job forms, and a **Bucket access** card on their pages. In OpenTofu:

  ```hcl
  resource "enclave_app" "storefront" {
    name          = "storefront"
    image         = "acme/shop:1"
    port          = 80
    bucket_access = [{ bucket = enclave_bucket.media.name, access = "read" }]
  }
  ```

  Rules: up to 10 buckets, each once, in the app's own project. The keys are kept in the secret `<app>-bucket-credentials` (read it with `enclave secret get <app>-bucket-credentials --reveal`). Changing the list keeps the keys; removing all access, or deleting the app, removes its user and its keys stop working at once. A bucket an app can access can't be deleted until the access is removed. With bucket access, the app's own `env` and `secretEnv` can't set the five variables. A job that runs as an app (`--app`) uses that app's access; a job from an image has its own.
- **Files in a bucket.** A bucket's page has an **Objects** tab: open folders, **Upload** files (several at once, into the folder shown or one you type, with optional tags such as `video-id`), **New folder**, **Download** and **Delete**. Only editors and owners of the project can see or change files. Uploads through EnclaveStack are up to 5 GiB; use an S3 client for bigger files. From the terminal:

  ```bash
  enclave bucket upload media ./clip.mov incoming/ --tag video-id=v42   # KEY ending in / is a folder; the file keeps its name
  enclave bucket objects media                     # folders and files at the top
  enclave bucket objects media incoming/ --recursive
  enclave bucket download media incoming/clip.mov ./copy.mov   # - writes to standard output
  enclave bucket delete-object media incoming/clip.mov --yes
  ```

  (`enclave bucket ls`, `get` and `rm` are about the bucket itself.) In OpenTofu a file is `enclave_bucket_object`:

  ```hcl
  resource "enclave_bucket_object" "intro" {
    bucket      = enclave_bucket.media.name
    key         = "videos/intro.mp4"
    source      = "${path.module}/intro.mp4"
    source_hash = filesha256("${path.module}/intro.mp4")   # a changed file is uploaded again
    tags        = { video-id = "intro" }
  }
  ```

  Use `content = "..."` instead of `source` for text. Destroying it deletes the file; import one with `tofu import enclave_bucket_object.intro media/videos/intro.mp4`.
- **The project's shared key.** The secret `objectstore-credentials` reaches every bucket of the project. EnclaveStack's own services use it; give it to an app only when it really needs every bucket.

**A transcoder: videos converted for you, and a message when each is done.** A transcoder watches a folder of a bucket and converts every file that lands there with FFmpeg. It writes three kinds of output, in any mix:

| Output | What you get | Set it with |
|---|---|---|
| **Presets** | Ready-made files: `mp4-480p`, `mp4-720p`, `mp4-1080p`, `webm-720p`, `hls-720p`, `mp3`, `thumbnail` | `--preset`, `presets` |
| **Renditions** | A file of the size and bitrate you choose, `<name>.mp4` (H.264 and AAC) or `<name>.webm` (VP9 and Opus) | `--rendition NAME:mp4:WIDTHxHEIGHT:KBPS[:AUDIO_KBPS[:KEYFRAME_SECONDS]]`, `renditions` |
| **An HLS ladder** | An adaptive stream: `<name>/master.m3u8` (what players load), and for each rung `<name>/<rung>/index.m3u8` and its segments. Every rung has a keyframe at the same moments, so a player can switch quality at any segment | `--hls-rung NAME:WIDTHxHEIGHT:KBPS` (repeatable), `--hls-segment`, `--hls-gop`, `--hls-audio`, `--hls-name`; `hls = { rungs = [...] }` |

Sizes are a box: the video keeps its shape, fits inside it and is never made larger than the original. 9:16 boxes (`720x1280`) suit phone video; 16:9 boxes (`1280x720`) suit landscape video. Defaults: audio 128 kbps, a keyframe every 2 seconds, 6-second segments (a whole number of keyframe intervals), ladder folder `hls`.

**Where outputs go.** Normally under `<output folder>/<file name>/`. Put `{name}` (the file's name without its extension) or `{meta:<tag>}` (a tag the uploader set, see below) in the output folder to choose it yourself: `processed/{meta:video-id}/` puts the outputs of a file tagged `video-id: v42` under `processed/v42/`. A file without the tag isn't converted; its run fails saying which tag is missing.

**Tags.** Set metadata on the upload the S3 way (`x-amz-meta-video-id: v42`; `Metadata={"video-id": "v42"}` in boto3, `Metadata: { "video-id": "v42" }` in the AWS SDK for JavaScript). Tag names are lowercase letters, digits and hyphens. The tags are copied onto every output, with `enclave-transcoder`, `enclave-run` and `enclave-source` (the input's key), and sent in the message.

**Being told when a file is done.** Give a webhook, a topic or both. When each file's run ends (succeeded, failed or timed out), EnclaveStack sends this JSON:

```json
{
  "event": "transcode.finished",
  "transcoder": "videos",
  "project": "default",
  "runId": "run_k3x…",
  "status": "succeeded",
  "message": "Converted incoming/v42.mov into 7 files.",
  "input": { "bucket": "media", "key": "incoming/v42.mov", "size": 48211337 },
  "outputs": [ { "bucket": "media", "key": "processed/v42/hls/master.m3u8", "size": 412 } ],
  "hlsMaster": "processed/v42/hls/master.m3u8",
  "metadata": { "video-id": "v42" },
  "startedAt": "2026-10-24T10:00:00Z",
  "finishedAt": "2026-10-24T10:01:12Z"
}
```

A webhook gets it as a POST with `Content-Type: application/json`, `X-Enclave-Event: transcode.finished`, `X-Enclave-Transcoder`, `X-Enclave-Run-Id` and, with a signing secret, `X-Enclave-Signature: sha256=<hex HMAC-SHA256 of the body with the secret's value>`. Check it before trusting the body:

```js
import { createHmac, timingSafeEqual } from 'node:crypto';
const expected = 'sha256=' + createHmac('sha256', process.env.HOOK_SECRET).update(rawBody).digest('hex');
const ok = timingSafeEqual(Buffer.from(expected), Buffer.from(req.headers['x-enclave-signature'] ?? ''));
```

A webhook that doesn't answer with a 2xx is tried 3 times (at once, after 2 and after 8 seconds); then an event on the transcoder says it failed, and the run keeps its own result. The URL must be `https://`, or `http://` to `localhost` or `127.0.0.1`. A topic gets the same body with the attributes `event` and `status`; subscribe a queue to it to process messages at your own pace. The secret and the topic a transcoder names can't be deleted while it does.

```bash
enclave secret create hook-key --value "$(openssl rand -hex 32)"
enclave transcoder create videos --input media/incoming/ --output 'media/processed/{meta:video-id}/' \
  --hls-rung 360p:360x640:600 --hls-rung 480p:480x854:1000 --hls-rung 720p:720x1280:2000 --hls-rung 1080p:1080x1920:4000 \
  --rendition selfie:mp4:720x1280:2500 \
  --notify-webhook https://video.example.com/hooks/transcode --notify-secret hook-key --wait
enclave transcoder update videos --hls-gop 3 --hls-segment 9    # the rungs and notify stay
enclave transcoder update videos --notify-topic video-events     # also publish to a topic
enclave transcoder update videos --no-hls --rendition ""         # remove the ladder and the renditions
enclave transcoder runs videos                                   # each file, its status and files written
```

```hcl
resource "enclave_transcoder" "videos" {
  name   = "videos"
  input  = { bucket = enclave_bucket.media.name, prefix = "incoming/" }
  output = { bucket = enclave_bucket.media.name, prefix = "processed/{meta:video-id}/" }
  renditions = [{ name = "selfie", container = "mp4", max_width = 720, max_height = 1280, video_kbps = 2500 }]
  hls = {
    rungs = [
      { name = "360p", max_width = 360, max_height = 640, video_kbps = 600 },
      { name = "720p", max_width = 720, max_height = 1280, video_kbps = 2000 },
    ]
  }
  notify = {
    webhook_url    = "https://video.example.com/hooks/transcode"
    webhook_secret = enclave_secret.hook.name
  }
}
```

In the console these are **Renditions**, **HLS ladder** and **Notify when a file is done** in the transcoder form, with a card for each on its page; a run's **Output** lists the files it wrote, the master playlist first. Outputs are stored with media types (`application/vnd.apple.mpegurl`, `video/mp2t`, `video/mp4`), so a CDN in front of the bucket serves them to players as they are.

**Moving from AWS Elemental MediaConvert.** There is no job API to call: write the upload to the transcoder's input folder (with your ID as a tag) instead of creating a job, and handle the webhook or topic message instead of the EventBridge `COMPLETE`/`ERROR` event. The message's `hlsMaster` is the playlist to store against your ID.

**A cache.** The page shows `redis://` host and port the same way.

## 10 · Day-to-day

### 10.1 Stopping and starting

- **Stop the server:** Ctrl-C in the terminal that runs `make start`. Your containers keep running; EnclaveStack just is not watching them.
- **Start it again:** `make start`. It reads `~/.enclave`, finds your resources and carries on. You do not set up again.
- **If Docker stops** (you quit Docker Desktop), the console shows a banner and changes queue up; they apply when Docker is back.

### 10.2 Where things are

| Where | What | Safe to delete? |
|---|---|---|
| `~/.enclave/` | EnclaveStack's database (`enclave.db`), its master key (`master.key`) and temporary files | **No.** This is all your resources and secrets. Without `master.key` your secrets cannot be read. Back up the whole folder while the server is stopped |
| `~/.enclave-demo/` | The same, for `make demo DRIVER=docker` | Yes, if you do not need the demo |
| `~/.config/enclave/config.json` | The server address and token `enclave login` saved | Yes (you sign in again) |
| `~/.enclave-opentofu-token` | The token `make tofu` reuses | Yes (it makes a new one) |
| `~/.tofurc.enclave` | Tells OpenTofu where the plug-in is | Yes (`make tofu` rewrites it) |
| `<repo>/build/` | The programs you built | Yes (rebuilt by `make start`) |
| Docker containers named `es-…` | Your resources | Delete them through EnclaveStack, not with `docker rm` |

### 10.3 Updating

```bash
git pull
pnpm install       # only if the console's libraries changed; harmless otherwise
make start         # rebuilds and starts; the database upgrades itself on the first start
make install       # so the enclave command matches the server
```

Stop the server before pulling. Back up `~/.enclave` first if the data matters.

### 10.4 Back up your data

Two layers: (1) copy `~/.enclave` (with the server stopped) to keep EnclaveStack's own state; (2) use `enclave backup create` (§7.4) to dump a database or cache to a bucket on a schedule, with restore from the console.

## 11 · Removing everything

In this order, so nothing is left running:

1. **Delete what you made**, through EnclaveStack: in the console, or `enclave ls` and `enclave delete <kind> <name> -y`, or `make tofu CMD=destroy` for OpenTofu folders. This removes the containers and their data.
2. **Stop the server** (Ctrl-C).
3. **Remove the leftovers** you no longer want: `rm -rf ~/.enclave ~/.enclave-demo ~/.config/enclave ~/.enclave-opentofu-token ~/.tofurc.enclave` (this deletes all EnclaveStack data for good).
4. **Remove the program:** `rm /opt/homebrew/bin/enclave` (or wherever `make install` said it put it; `which enclave` shows it), and delete the repository folder.
5. **Check Docker**: `docker ps -a --filter label=enclave.managed` should be empty. Anything left there can be removed with `docker rm -f <name>`; `docker network ls --filter label=enclave.managed` and `docker volume ls --filter label=enclave.managed` show networks and volumes.

## 12 · Troubleshooting

| What you see | Why | Fix |
|---|---|---|
| `make: *** No rule to make target 'start'` | You are not in the repository folder, or the code is old | `cd` into the `enclavestack` folder; `git pull` |
| `make start` fails early with `go: command not found` / `pnpm: command not found` | A tool from §2 is missing | Install it, open a new terminal, check with the table in §2.1 |
| Build fails with "requires go >= 1.26" or a Node version error | The tool is too old | Upgrade Go or Node |
| The console shows a banner "engine unavailable" | Docker is not running | Start Docker Desktop (Mac) or the Docker service (Linux); queued work resumes |
| `listen on 127.0.0.1:7070: address already in use` | Another server (or demo) is running | Stop it, or `make start ARGS='--listen 127.0.0.1:7171'` |
| Setup page asks for a code you do not have | You closed the terminal that printed it | Restart the server; a new code is printed |
| `enclave: command not found` | The terminal does not know the command | `make install`, then open a new terminal (or `hash -r`); or use `./build/enclave` |
| `enclave` says "not signed in" or "401" | No saved token, or it was revoked | `enclave login` |
| `enclave` says it cannot connect | The server is not running, or it is on another port | `make start`; or `--server` / `ENCLAVE_SERVER` |
| A resource stays `provisioning`, then `failed` | The image could not be pulled, or the container exited | `enclave events <kind> <name>` and `enclave logs <kind> <name>`; fix and `enclave retry <kind> <name> --wait` |
| `make tofu`: "Nothing answers at http://127.0.0.1:7070" | The server is not running | Run `make start` in another terminal |
| `make tofu`: "OpenTofu isn't installed" | `tofu` is not on your PATH | `brew install opentofu` or the Linux instructions in §2.2 |
| `tofu` says the provider `enclavestack/enclave` cannot be found | The dev override file is not in use | Use `make tofu`, or follow §8.7 exactly |
| `tofu plan` says a resource "already exists" | The state file was deleted or belongs to another folder | Delete the existing resource (console or `enclave delete`), or restore the state file |
| A published port does not answer from another device | Ports bind to `127.0.0.1` by default | `--publish-address 0.0.0.0` (and mind your network) |
| `make demo` or `make start` reports Docker socket errors on a Mac | Docker Desktop is not up yet | Wait until Docker Desktop says it is running; EnclaveStack also looks for `~/.docker/run/docker.sock` |

If a problem is not here, `enclave events <kind> <name>`, `enclave logs <kind> <name>` and the server's own terminal output say what happened; `--log-level debug` adds detail.

## 13 · Words used in this guide

| Word | Meaning |
|---|---|
| **Resource** | Anything EnclaveStack manages: an app, a database, a secret, a domain… |
| **Kind** | The type of a resource (`app`, `database`, `cache`…) |
| **Server** | The running `enclave server` program: the console, the API and the thing that drives Docker |
| **Console** | The web page the server serves |
| **Engine / driver** | What actually runs resources: `docker` (real containers) or `fake` (simulated, for the demo) |
| **Project** | A separate set of names, so two things can share a name |
| **Token** | A password-like string for scripts, CI and OpenTofu; shown once; has a role |
| **Setup code** | The one-time code that lets the first administrator claim a fresh server |
| **Plan / apply** | OpenTofu's two steps: show the difference, then make it real |
| **State file** | OpenTofu's memory of what it created; never commit it |
| **Machine** | A computer EnclaveStack runs resources on. The one you started it on is the *main machine*; you can add more |
| **Stack** | A product installed from the catalogue as one unit: an app with its database, storage and domain |
| **DNS** | The internet's phone book: turns a name such as `dev.galiaraa.com` into an address (§15) |
| **NAT / CGNAT** | Your router (NAT), and often your internet provider too (CGNAT), hiding your devices behind a shared address, so nothing outside can start a connection to them (§15.2) |
| **Edge** | A server on the internet with a public address that receives visitors for you: Cloudflare's, Tailscale's or ngrok's network, or your own rented server (§15.3) |
| **Tunnel** | A line your computer opens **out** to an edge and keeps open, so visitors' requests can come back down it (§15.3) |
| **Connector** | The small program at your end of a tunnel, run by EnclaveStack in a container |
| **Edge proxy** | EnclaveStack's receptionist (Caddy) that hands each request to the right app by its name and serves HTTPS |
| **Edge machine** | A rented server joined to EnclaveStack only to relay visitors to your computer; nothing else runs on it (§15.10) |
| **Gate** | What stands in front of a public app: sign in with EnclaveStack, a shared password, or open to everyone (§15.5) |

## 14 · What each detail means

Every page in the console shows a resource's **Details**: each line is a label, its value and, under the label, what it is for. Values you are likely to paste somewhere (addresses, names of secrets, keys) have a **Copy** button, and a secret's name links to that secret. This is the same list, so you can look a detail up here too. `enclave get <kind> <name>` shows the same values.


**Reaching it**

| Detail | What it means |
|---|---|
| Host | The address to use from this computer (your scripts, a database client). |
| Port | The port on this computer. It is chosen once and stays the same. |
| Internal host | The name apps inside EnclaveStack use to reach it. Prefer this between your apps. |
| Internal port | The port apps inside EnclaveStack use with the internal host. |
| Native port | The engine’s own protocol port on this computer (ClickHouse native clients). |
| Internal native port | The native protocol port apps inside EnclaveStack use. |
| URL | Open this in a browser, or call it from code. |
| Internal URL | The address apps inside EnclaveStack use. |
| S3 endpoint | The S3 address for code and tools on this computer (AWS CLI, SDKs with path-style addressing). |
| Internal S3 endpoint | The S3 address apps inside EnclaveStack use. It is what AWS_ENDPOINT_URL is set to. |
| Public address | The address on the internet that points at this machine. |
| Private IP | Its address inside its network. |
| Private DNS name | A name that resolves to it inside its network. |
| SSH address | Where to connect with ssh; the SSH card shows the full command. |
| SSH user | The user to log in as. |
| Message viewer | A page that shows every message this email service caught. |
| SMTP port (internal) | The port apps inside EnclaveStack send mail to. |
| Fetches from | Where the CDN gets what it caches. |
| Gateway | The address traffic leaves the network through. |
| Engine network | The Docker network that carries this network’s traffic. |
| Name servers | Set these at your domain registrar so the zone answers for your domain. |

**Secrets and keys**

| Detail | What it means |
|---|---|
| Password secret | The secret that holds the password. Read it with “enclave secret get NAME --reveal”, or give it to an app as a secret environment variable. |
| Credentials secret | The secret holding the keys ({"accessKey", "secretKey"}). This shared key reaches every bucket in the project; give apps keys of their own with bucket access instead. |
| Bucket keys | The secret holding this app’s own keys, which reach only the buckets in its bucket access. |
| Username | The user to sign in as. |
| Database | The database name to connect to. |
| Bucket | The bucket’s name, as S3 clients use it. |
| Value updated | When the secret’s value last changed. |
| Credentials checked | When EnclaveStack last checked it can sign in to the store. |
| Store | The secret store this value comes from. |
| Key | The name of the value in the secret store. |
| Secrets synced | How many secrets are kept in step with this store. |
| Last synced | When the value was last read from the store. |

**What it is**

| Detail | What it means |
|---|---|
| Engine | The software that runs it, and its version. |
| Version | The version that runs now. |
| Image | The container image or machine image it runs. |
| Source | The Git repository and branch it is built from. |
| Built commit | The commit the running image was built from. |
| Built image | The image built from the source. |
| Last build | How the last build went; the Build log tab shows it. |
| Serving revision | The revision that answers requests now. |
| Last deploy | How the last change was rolled out. |
| Network | The private network it joins. Resources on the same network reach each other by internal name. |
| Machine | The machine it runs on. |
| Replicas | How many copies run at once. |
| Running replicas | How many copies run right now. |
| Memory | The most memory it may use; past it, it is restarted. |
| Max memory | The most memory the cache keeps; older keys are dropped past it. |
| Published | Whether it is reachable from this computer on a fixed port, not only inside EnclaveStack. |
| Deploy strategy | How a change rolls out: rolling keeps it serving; recreate stops it first. |
| Health check | How EnclaveStack decides it is working. |
| Versioning | Whether old versions of changed or deleted files are kept. |
| Size | How big it is. |
| Root disk | The size of the VM’s main disk. |
| Disks | Extra disk volumes attached to the VM. |
| Published ports | VM ports reachable from this computer. |
| Power | Whether the VM is running. |
| Image SHA-256 | The checksum of the machine image it started from. |
| Kind of volume | A filesystem for apps and jobs, or a disk for one VM. |
| Attached to | What uses this volume now. |
| Space used | How much of it holds data. |
| Started from snapshot | The snapshot its data came from. |
| Copy of | The volume or VM this snapshot copies. |
| Type | What kind it is. |
| Size on disk | The space it takes on this machine. |
| Taken | When it was made. |
| Copied while in use | Yes means it was copied while running; files being written may be incomplete. |
| Restored from | The backup this was restored from. |

**Messaging and media**

| Detail | What it means |
|---|---|
| Visibility timeout | How long a received message stays hidden from other workers before it is delivered again. |
| Retention | How long messages are kept. |
| Largest message | The biggest message it accepts. |
| Dead letters | What happens to a message received too many times without being deleted. |
| Delivers to | Where each message published to the topic goes. |
| Subject | The broker subject apps publish and subscribe to. |
| Stream | The broker stream that keeps the messages. |
| Consumer | The broker consumer workers receive through. |
| Messages waiting (about) | Messages ready to be received. |
| In flight (about) | Messages received but not yet deleted. |
| Subscriptions | Queues and webhooks that get each message. |
| Mode | Capture keeps mail here; relay also delivers it through your provider. |
| Relay | The provider mail is delivered through. |
| Sender domain | The only domain mail may come from; Any allows all. |
| Messages kept | How many caught messages are kept. |
| Watches | The bucket folder it watches for new files. |
| Watching | Whether new files are converted automatically. |
| Output | Where converted files go. {name} and {meta:<tag>} are filled in from each file. |
| Presets | Ready-made outputs it writes for each file. |
| Limits | Files at once, memory per file and the longest one file may take. |
| Hostname | The name it answers on. |
| Origin | The app or bucket it caches. |
| HTTPS | Where its certificate comes from. |
| HTTP | What plain HTTP requests get. |
| Certificate | The state of its HTTPS certificate. |
| Certificate expires | When the certificate runs out; it is renewed before then. |
| Issued by | Who issued the certificate. |
| Default cache time | How long a response is kept when the origin doesn’t say. |
| Cache size | The most it keeps; the least used is dropped first. |
| Cached | How much it holds now. |

**Networking**

| Detail | What it means |
|---|---|
| Address range | The private addresses the network hands out. |
| Internet | Whether resources on it can reach the internet. |
| Subnets | Smaller ranges inside the network. |
| Protects | What this firewall’s rules apply to. |
| Other outgoing traffic | Traffic no rule mentions: allowed or denied. |
| Containers protected | How many containers the rules are on now. |
| Rules applied | When the rules were last put in place. |
| Listens on | The protocol and port it accepts. |
| Sends to | Where it sends traffic or data. |
| Algorithm | How it picks a replica. |
| Replica check | How it checks a replica can take traffic. |
| Replicas found | Replicas behind it. |
| Healthy replicas | Replicas that pass the check. |
| Zone | The DNS zone it belongs to. |
| Zone ID | The zone’s ID at the DNS provider. |
| Provider | The DNS provider that holds the zone. |
| Record | The name and type of the DNS record. |
| Full name | The record’s whole name. |
| Points at | What the record resolves to. |
| Values at the provider | What the provider holds now. |
| TTL | How long resolvers may cache the answer. |
| App | The app it routes to. |

**Scheduling and runs**

| Detail | What it means |
|---|---|
| Schedule | When it runs. |
| Runs with | The app or image each run uses. |
| Command | What each run executes. |
| Timeout | The longest one run may take before it is stopped. |
| If still running | What happens when the next run is due before the last one ends. |
| Next run | When it runs next. |
| Last run | When it last ran. |
| Last run result | How the last run ended; the Runs tab has each run. |
| Stored in | Where the backups are written. |
| Keeps | Which backups are kept; older ones are deleted. |
| Key prefix | The folder the backups are written under. |
| Last successful backup | When the last backup that worked finished. |

**Monitoring**

| Detail | What it means |
|---|---|
| State | Whether the alert is firing now. |
| Since | When it entered this state. |
| Last checked | When the condition was last checked. |
| Last delivery | How the last notification went. |
| Last delivered | When a notification last went out. |
| Lines sent | Log lines sent so far, and their size. |
| Sent | How much was sent. |
| Behind by | How far sending lags behind the logs. |
| Last sent | When lines were last sent. |
| Last error | Why the last attempt failed. |
| Sources | Whose logs are sent. |
| Only lines matching | Only log lines containing this are sent. |
| Field | The part of the stored value used. |
| Widgets | Graphs on the dashboard. |
| Created | When it was made. |

**Stacks and backups**

| Detail | What it means |
|---|---|
| Template | The catalogue template the stack was made from. |
| Template version | The template version it runs; a newer one can be applied with an update. |
| Members ready | How many of the resources it made are ready. |
| Stack output | A value the template reports, such as an address to open. |
| Backup file | The file this backup run wrote. |
| SHA-256 | A checksum; restoring checks the file still matches it. |

**Machines**

| Detail | What it means |
|---|---|
| Name in commands | What the CLI, OpenTofu and placement use (machine/<name>). It never changes. |
| ID | Its permanent ID. |
| State since | When it entered its current state. |
| Last heard | When its agent last answered. |
| Address | Where the main machine reaches its agent. |
| System | Its operating system and processor. |
| Agent | The EnclaveStack version its agent runs. |
| Machine engine | The container engine on it, and its version. |
| CPUs | Processor cores EnclaveStack may use. |
| VMs | Whether it can run virtual machines. |
| Role | Worker machines run resources; an edge machine only relays visitors for relay tunnels. |
| Relays | The relay tunnels that run on this edge machine. |

**Public access**

| Detail | What it means |
|---|---|
| Way in | How visitors reach it from the internet. |
| Connection | Whether its connector is connected now; reconnecting means it is trying again. |
| Connections | How many connections the connector holds to the service. |
| Tunnel ID | The tunnel’s ID at Cloudflare. |
| Account ID | The Cloudflare account the tunnel is made in. |
| Edge machine | The server with a public address the relay runs on. |
| Who can open it | Private: only your tailnet’s devices. Public: anyone, behind the gate. |
| Device name | Its name in your tailnet. |
| Public with Funnel | Yes means anyone on the internet can open it; no means only your tailnet’s devices. |
| Gate | Who may open it: people who sign in, a shared password, or everyone. |
| Open paths | Paths that skip the gate, for webhooks. |
| Public | Whether it is served to the internet through a tunnel. |
| Tunnel | The tunnel it is served through. |
| Tunnel connection | Whether the tunnel it goes through is connected now. |
| Cloudflare Access | The people Cloudflare checks with a one-time code before the gate. |
| Access application | The Cloudflare Access application that checks visitors first. |
| DNS record | The record that sends visitors to the tunnel, and whether EnclaveStack made it. |

## 15 · Reach your apps from the internet

> *`L2-014`. Everything here works from the console, the CLI and OpenTofu.*

This section explains how someone on a phone, anywhere, can open an app running on your computer at home, and why that doesn't put your computer at risk. Read the first part once even if you just want the commands: the setup choices only make sense once the picture is clear.

### 15.1 How a website visit works

Opening `https://stage.galiaraa.com` on a phone takes two steps:

1. **Find the address.** The phone asks **DNS**, the internet's phone book: "where is `stage.galiaraa.com`?" DNS answers with an **IP address**, such as `104.16.5.20`. Your **domain** (`galiaraa.com`) is your listing in that phone book, which you rent from a registrar.
2. **Connect to it.** The phone opens a connection to that address on **port 443** (HTTPS) and asks for the page. The connection is encrypted (**TLS**: the padlock in the browser).

So a public website needs a phone-book entry, and a computer at that address that accepts connections from strangers.

### 15.2 Why your computer at home can't be reached

```
                       THE INTERNET
                            │
          ┌─────────────────┴──────────────────┐
          │ Your provider's CGNAT              │  one public address shared by
          │ (e.g. 49.36.12.7)                  │  many homes — you can't configure it
          └──────┬─────────────┬───────────┬───┘
                 │             │           │
            neighbour    ┌─────┴─────┐  neighbour
                         │Your router│  hides your devices behind one address (NAT)
                         └─────┬─────┘
                     ┌─────────┼─────────┐
                    Mac      phone       TV       private addresses (192.168.1.x)
```

- **Your router** gives every device at home a private address. It lets your devices start connections **out**, and lets the replies back in. A connection that starts **outside** is dropped, because the router doesn't know which device it's for and never asked for it. This is called **NAT**.
- **Many providers** go further. They share one public address between many homes (**CGNAT**), so even telling your router to forward a port ("port forwarding") doesn't help: the provider's own equipment drops the connection first.
- **Your home address changes** from time to time.

What always works is going **out**. Everything below uses that.

### 15.3 The trick: your computer calls out first

```
   Visitor's phone
         │  ② "stage.galiaraa.com" → DNS → the edge's address
         ▼
 ┌──────────────────────────┐
 │  EDGE on the internet     │  ③ checks the visitor, then sends the request
 │  (Cloudflare, Tailscale,  │     down the line your computer opened
 │   ngrok, or your server)  │
 └────────────▲─────────────┘
              │  ① your computer dials OUT and keeps the line open
              │    (the router allows it: your computer started it)
      provider CGNAT → your router
              │
 ┌────────────┴──────────────────────────────────────────┐
 │ YOUR COMPUTER                                          │
 │  connector ──▶ edge proxy (Caddy) ──▶ the app ──▶ its database
 │   (one end      ④ picks the app by    (a container)    (private, never public)
 │    of the line)    its name, checks
 │                    the gate
 └────────────────────────────────────────────────────────┘
```

1. When you create a **tunnel**, EnclaveStack starts a small program, the **connector**, which dials **out** to an **edge**, a server on the internet that has a real public address. The line stays open. Your router and your provider allow it, because your computer started the call.
2. Your domain's DNS record points visitors at the **edge**, not at your home.
3. The edge sends each visitor's request **down the line your computer opened**.
4. At home, EnclaveStack's **edge proxy** (a receptionist that forwards each request by its name) checks the **gate**, then hands the request to the right app. The answer goes back the same way.

Nothing on your router is opened, and your home address never appears anywhere public.

### 15.4 The ways in: pick one from the catalogue

Every way in is installed from **Catalogue › Public access**, and any app is made public with **Make public** on its page. They come in two families.

**An address for one app**: the service hands out the address.

| | **Quick link** | **Tailscale** | **ngrok** | **Custom** |
|---|---|---|---|---|
| Your address | `random-words.trycloudflare.com`, changes on every restart | `galiaraa-stage-web.your-tailnet.ts.net`, stays the same | one address per ngrok account | whatever that service gives |
| You need | nothing | a free Tailscale account | a free ngrok account | that service's container image |
| Who can open it | anyone with the link (behind the gate) | **private**: only your own devices with Tailscale; or **public**: anyone (behind the gate) | anyone with the link (behind the gate) | as that service allows |
| Limits | 200 requests at once; for demos | Tailscale limits public bandwidth; not for production | about 1 GB and 20,000 requests a month, and a warning page first | that service's |
| Good for | a five-minute demo | dev and stage for your team, every day | a quick share | a service EnclaveStack doesn't know |

**Your own domain** (`stage.galiaraa.com`):

| | **Cloudflare tunnel** | **Your own server (relay)** |
|---|---|---|
| The edge is | Cloudflare's network | a small server you rent (a VPS), joined as an **edge machine** |
| You need | a free Cloudflare account, and your domain's DNS at Cloudflare | a VPS with a public address (about $4–6 a month); DNS anywhere |
| Who can read the traffic | Cloudflare (it serves the certificate) and you | only you: the server passes encrypted bytes and never holds a key |
| Good for | dev and stage on your domain, day to day | when no third party may see your traffic |

EnclaveStack runs no edge of its own and no DNS server. Its job is the glue: start the connector or relay, write the routing rules and DNS records, and run the gate. Vercel-style hosts can't do this job: their addresses point at code they host, and they can't reach a computer at home.

### 15.5 Why this is safe

| Layer | What protects you |
|---|---|
| Your home network | Nothing is opened. Scanners that probe your home address find nothing. |
| The edge | Floods of junk traffic stop at Cloudflare or your server, never reaching home. With Cloudflare you can also add **Cloudflare Access**: Cloudflare emails a one-time code to the people you list before letting them through. |
| The tunnel | Each tunnel reaches **only the names you made public** through it. Anything else gets "not found". The console, the API and your computer's files are never reachable this way. |
| The gate | Every public app has a gate, **Sign in** by default: only people who have access to that project in EnclaveStack get past it. You can share a password instead, or open it to everyone after confirming. |
| The app | Your app runs in its own container. Its database is on a private network and is never made public. A bug in the app lets someone into that container, not onto your computer. |

Things that stay your job: keep dev and stage behind a gate, keep the console private, and remember that public access is only up while your computer (and, for a relay, your server) is on. That's fine for dev and stage; production belongs on a machine that's always on.

### 15.6 A quick link in one minute

No account and no domain needed:

```sh
enclave tunnel create demo --provider quick --app web --wait
enclave tunnel get demo        # shows url: https://…trycloudflare.com
```

Open the URL on any device. You'll see EnclaveStack's sign-in page first; sign in with your EnclaveStack email and password. The address changes whenever the link restarts, and it handles at most 200 requests at once. Delete it with `enclave tunnel delete demo`.

### 15.7 Tailscale: a stable address, private or public

Tailscale is a free app that puts your devices on one private network (a **tailnet**), wherever they are. It uses the same encryption EnclaveStack uses between machines (WireGuard). EnclaveStack can add an app to your tailnet as a device of its own, with a stable address such as `https://galiaraa-stage-web.your-tailnet.ts.net`.

- **Private** (Tailscale *Serve*): only devices signed in to your tailnet can open it. The app isn't on the internet at all. That's the safest way for you and your team to use dev and stage from your phones.
- **Public** (Tailscale *Funnel*): anyone with the link can open it, behind EnclaveStack's gate. Use it to show a client.

**Once, at tailscale.com:** sign up, install Tailscale on your phone and laptop, then in the admin console:
1. **DNS**: turn on **MagicDNS** and **HTTPS Certificates**.
2. For public links: allow Funnel in **Access controls** (Tailscale's own instructions show the `funnel` attribute to add).
3. **Settings › Keys › Generate auth key**: reusable, with a tag such as `tag:enclave`.

**Then:**
```sh
enclave project use galiaraa-stage
enclave secret create ts-key --value '{"authKey":"tskey-auth-…"}'
enclave tunnel create stage-ts --provider tailscale --credentials ts-key --app web --mode private --wait
enclave tunnel get stage-ts        # url: https://galiaraa-stage-web.<your tailnet>.ts.net
```
Open it on your phone with Tailscale on. When a client needs it: `enclave tunnel update stage-ts --mode public --gate password --gate-password-secret stage-pass`.

In the console: **Catalogue › Public access › Tailscale › Install**, or **Make public** on the app's page.

### 15.8 ngrok and custom connectors

- **ngrok**: `enclave secret create ngrok --value '{"authToken":"…"}'`, then `enclave tunnel create stage-ngrok --provider ngrok --credentials ngrok --app web --wait`. The free plan gives one address per account and shows visitors a warning page first.
- **Any other service** (`custom`): name its container image and command. EnclaveStack sets `ENCLAVE_UPSTREAM`, the address the connector must send traffic to:
  ```sh
  enclave tunnel create stage-x --provider custom --app web --image <its image> --command "<its command using \$ENCLAVE_UPSTREAM>" --url https://<the address it gives>
  ```
  The gate still stands in front of your app.

### 15.9 Stage on your own domain, with Cloudflare, while prod stays on AWS

This is the Galiaraa setup: production runs on AWS, and **stage** runs on EnclaveStack in the project `galiaraa-stage`, with an app `web`. One EnclaveStack environment uses your whole domain's zone. EnclaveStack only ever writes the record of its own domain (`stage`), and never changes prod's records.

**Once: move your domain's DNS to Cloudflare** (free). Your domain can stay registered where it is, and prod keeps running on AWS.
1. Sign up at cloudflare.com, choose **Add a site**, type `galiaraa.com`, pick the Free plan. Cloudflare copies your existing records, prod's included. **Check them against your current DNS** (Route 53 or wherever it is now) before going on. A Route 53 "alias" to a load balancer becomes a `CNAME` to the load balancer's name, which Cloudflare allows at the apex too.
2. Cloudflare shows two **name servers**. At your registrar, replace the domain's name servers with those two. It takes minutes to a day, and prod keeps answering throughout, because both sides hold the same records.
3. Make an API token: **My Profile › API Tokens › Create Token › Custom token**, with Account › **Cloudflare Tunnel › Edit**, Zone › **DNS › Edit** (zone `galiaraa.com`), and Account › **Access: Apps and Policies › Edit** if you'll use Cloudflare Access. Note your **account ID** from the domain's overview page.

**Then, in the stage project:**
```sh
enclave project use galiaraa-stage
enclave secret create cf --value '<token>'
enclave zone create galiaraa --provider cloudflare --zone galiaraa.com --credentials cf --wait
enclave tunnel create home --provider cloudflare --credentials cf --account-id <account id> --wait
enclave domain create stage.galiaraa.com --app web --tunnel home --zone galiaraa --gate signin --wait
```
One token serves the zone and the tunnel; EnclaveStack never stores the tunnel's own run token, it asks Cloudflare for it each time it starts the connector. EnclaveStack creates the tunnel (Cloudflare shows `enclave-galiaraa-stage-home` as **Healthy** under **Zero Trust › Networks › Tunnels**), writes the one record `stage`, and routes it. Open `https://stage.galiaraa.com` on your phone on mobile data: sign in, and you're on stage.

**Don't want to move prod's DNS?** Use your own server instead (§15.10). It works with the zone where it is now, Route 53 included, and EnclaveStack adds only the `stage` record there.

**DNS at GoDaddy or another provider EnclaveStack doesn't write to?** Keep the domain registered there; only its DNS matters. You have three choices:
- **Move the DNS to Cloudflare** (free), as above. At GoDaddy: **My Products › your domain › DNS › Nameservers › Change › I'll use my own nameservers**, then enter Cloudflare's two. Before you switch, check that Cloudflare's copy has your **MX** and **TXT** records (email and verifications). Set prod's imported records to **DNS only** (grey cloud) so prod behaves exactly as before.
- **Leave the DNS where it is and use your own server** (§15.10) **without a zone**. EnclaveStack shows the one record to add: an `A` record `stage` → your server's IP. Add it once in GoDaddy's DNS page. The server's IP doesn't change, so it never needs touching again.
- **Move the DNS to Route 53** (about $0.50 a month) and use your own server, with EnclaveStack writing the record.

A Cloudflare tunnel can't work while the DNS stays at GoDaddy, because Cloudflare only serves names in zones it hosts; EnclaveStack refuses it with that message.

Giving a client access to stage without an EnclaveStack account:
```sh
enclave secret create stage-pass --value '{"username":"client","password":"<a long password>"}'
enclave domain update stage.galiaraa.com --gate password --gate-password-secret stage-pass
```
Letting a payment provider's webhooks through the gate: `--gate-open-path /webhooks/`.

In the console the same is: **Tunnels › New tunnel › Cloudflare**, then on a domain, **Public access › From the internet**.

**More than one environment** (dev and stage, say). Each environment is its own project with its own zone and its own tunnel; nothing is shared between projects. Each project's zone is a **subdomain zone**, `dev.galiaraa.com` and `stage.galiaraa.com`: a zone of its own at your DNS provider, handed over from `galiaraa.com` by two `NS` records there.
- **Route 53:** create a hosted zone `stage.galiaraa.com` (about $0.50 a month), copy its four name servers into an `NS` record `stage` in `galiaraa.com`, and use that zone in the stage project with a relay (§15.10).
- **Cloudflare:** a subdomain zone needs Cloudflare's Enterprise plan. Then it works exactly as above, with `--zone stage.galiaraa.com`.

### 15.10 Your own server instead of Cloudflare

1. Rent a small Linux server with a public IPv4 address and install Docker and the same EnclaveStack version. In its provider's firewall, open TCP 80, 443 and 7071 and UDP 51820.
2. On your computer: `enclave machine add vps1 --edge --address <the server's IP>`. It prints a command.
3. On the server, run that command: `enclave agent join --edge --code <code>`. It waits; your computer connects to it (your computer can always reach the server, never the other way round), and within seconds `vps1` is **Online** as an **Edge** machine. Nothing else will ever run on it.
4. In the stage project: `enclave tunnel create edge --provider relay --machine vps1 --wait`
5. Add your existing zone, wherever it is (Route 53 here), and the domain:
   ```sh
   enclave secret create r53 --value '{"accessKeyId":"…","secretAccessKey":"…"}'
   enclave zone create galiaraa --provider route53 --zone galiaraa.com --credentials r53 --wait
   enclave domain create stage.galiaraa.com --app web --tunnel edge --zone galiaraa --wait
   ```
   EnclaveStack writes one `A` record, `stage`, pointing at the server; prod's records stay as they are. Without a zone, it tells you the record to add yourself.

The certificate is a normal Let's Encrypt one, made and kept at home. The server only passes encrypted bytes along, so it never holds a key or sees a page. One edge machine carries one environment; another environment needs another small server.

### 15.11 When something's wrong

| You see | What it means | What to do |
|---|---|---|
| Tunnel **Reconnecting** | The connector lost its line to the edge (your internet dropped, or Cloudflare is having trouble) | Nothing; it reconnects by itself. If it lasts, check your internet |
| "Cloudflare tunnels need the domain's DNS at Cloudflare" | The domain's zone is at another provider | Move the DNS to Cloudflare (§15.9) or use a relay (§15.10) |
| "stage.galiaraa.com isn't a zone in this … account" | You added a subdomain zone that doesn't exist at the provider | Create it there first (Route 53 hosted zone; Cloudflare Enterprise), or use the whole domain for a single environment (§15.9) |
| "The token can't edit tunnels: add …" | The API token is missing a permission | Edit the token at Cloudflare, add what it names, then **Retry** |
| DNS record **Different value** | A record with that name already exists and points elsewhere | Delete or change it at your DNS provider; EnclaveStack never overwrites it |
| "This environment is private." | You signed in, but you have no role in that project | Ask an owner of the project to add you |
| The quick link stopped working | It restarted and got a new address | Copy the new one from `enclave tunnel get` or the console |
| Tailscale link "Turn on HTTPS Certificates…" | Your tailnet isn't set up for it yet | Do the steps in §15.7, then **Retry** |
| Tailscale link opens on your laptop but not a friend's phone | It's **private** | Make it **public** (behind the gate), or invite them to your tailnet |
| Edge machine **Waiting for the server** | The join command hasn't run, or a port is closed | Run the command on the server; check TCP 7071 is open to the internet |

## 16 · Use a service that runs elsewhere, like Supabase

> *`L2-015`. Everything here works from the console, the CLI and OpenTofu.*

Your apps can use services that run somewhere else, such as a hosted Supabase project. EnclaveStack doesn't run them. It keeps their address and keys as secrets, so you paste them once and attach them to any app. The catalogue's **Connections** group holds these.

### 16.1 Connect Supabase

1. In the Supabase dashboard, copy the **Project URL** (like `https://abcd.supabase.co`) and the **anon** key. Copy the **service_role** key too if a server app needs it. If an app talks to Postgres directly, copy the **pooler** connection string under **Connect**: the direct one needs IPv6, which many home networks don't have.
2. Open **Catalogue › Connections › Supabase › Install**. Name it, for example `supabase`, and paste the address without `https://` and the keys. Leave out the keys you don't need.
3. The stack's page lists under **Outputs** which secret holds each variable:

| Variable | Secret |
|---|---|
| `SUPABASE_URL` | `supabase-url` |
| `SUPABASE_ANON_KEY` | `supabase-anonkey` |
| `SUPABASE_SERVICE_ROLE_KEY` | `supabase-servicekey` (if you gave it) |
| `DATABASE_URL` | `supabase-dburl` (if you gave it) |

From the CLI:

```sh
enclave stack create supabase --template supabase --param project=abcd.supabase.co \
  --secret-param anonKey=… --secret-param serviceKey=…
enclave stack get supabase          # the outputs
```

In OpenTofu:

```hcl
resource "enclave_stack" "supabase" {
  name              = "supabase"
  template          = "supabase"
  parameters        = { project = "abcd.supabase.co" }
  secret_parameters = { anonKey = var.supabase_anon_key, serviceKey = var.supabase_service_key }
}
```

### 16.2 Use it from an app

Give the app the secrets as secret environment variables:

```sh
enclave app create api --image ghcr.io/you/api:1.0 --port 8080 \
  --secret-env SUPABASE_URL=supabase-url \
  --secret-env SUPABASE_SERVICE_ROLE_KEY=supabase-servicekey
```

The app reads them like any other variable. Keep the `service_role` key on servers only: it skips row-level security.

### 16.3 Change a key

Open the stack, choose **Edit parameters**, paste the new key and save (keys you leave empty stay as they are). Then restart the apps that use it, since an app reads its variables when it starts. You can't delete the stack while an app still uses one of its secrets.

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-23 | Soupler | The complete guide: what it is, requirements and installing tools, getting the code, `make demo` and `make start`, first-run setup, the console, the `enclave` command, OpenTofu and `make tofu`, connecting your code, day-to-day, removal, troubleshooting, glossary |
| v2 | 2026-10-24 | Soupler | §9: per-app bucket access (keys of its own, the five variables, CLI, console, OpenTofu, rules); the shared project key as the fallback |
| v3 | 2026-10-24 | Soupler | §9 transcoders: presets, renditions, HLS ladder, output folders from tags, the done message, signature check, CLI and OpenTofu, moving from MediaConvert (L2-012) |
| v4 | 2026-10-24 | Soupler | §9 files in a bucket (console, CLI, OpenTofu); §14 what each detail means (L2-013) |
| v5 | 2026-10-24 | Soupler | §15 reach your apps from the internet: how a visit works, NAT and CGNAT, the tunnel, the ways in from the catalogue (quick link, Tailscale, ngrok, custom, Cloudflare, your own server), why it's safe, a quick link, Tailscale private and public, ngrok and custom, stage beside prod on AWS with Cloudflare (moving DNS, token, commands), more environments as subdomain zones, DNS at GoDaddy or another provider, your own server with Route 53, troubleshooting; glossary words (L2-014) |
| v6 | 2026-10-25 | Soupler | §16 use a service that runs elsewhere: connect Supabase, use it from an app, change a key (L2-015) |
