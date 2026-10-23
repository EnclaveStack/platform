# L5-004 · User Guide: install, run and use EnclaveStack

---
| Field | Value |
|---|---|
| Document ID | L5-004-user-guide-v1 |
| Layer | L5 — Operations |
| Status | Draft |
| Version | v1 |
| Created | 2026-10-23 |
| Last Updated | 2026-10-23 |
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

**A bucket.** Its page shows an S3 endpoint and access keys. Any S3 client or SDK works with that endpoint.

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

## Version history

| Version | Date | Author | Summary of changes |
|---|---|---|---|
| v1 | 2026-10-23 | Soupler | The complete guide: what it is, requirements and installing tools, getting the code, `make demo` and `make start`, first-run setup, the console, the `enclave` command, OpenTofu and `make tofu`, connecting your code, day-to-day, removal, troubleshooting, glossary |
