import type { SectionSlug } from './sections';

/**
 * A guide per section (CON-67, L2.5-002 §16): what it is, when to use it, how, an example and what's good to
 * know. Plain words about EnclaveStack only — guides never compare it with other companies' products.
 * `command` is one `enclave` command that works as written against `make demo` once the resources its example
 * names exist; zones and secret stores also need a real account at their provider, so they don't wait.
 */
export interface Guide {
  what: string[];
  when: string[];
  how: string[];
  command: string;
  example: string[];
  note: string[];
  related: SectionSlug[];
}

export const GUIDES: Record<Exclude<SectionSlug, 'records'>, Guide> = {
  apps: {
    what: [
      'An app is a program that runs all the time: a website, an API, a background worker. You give EnclaveStack a container image, or a Git repository with a Dockerfile, and it keeps the app running.',
      'If the app stops, EnclaveStack starts it again. When you change it, the new version starts next to the old one and takes over only once it answers, so visitors never see it go down.',
    ],
    when: [
      'You have a website or API to put online.',
      'You run a worker that reads from a queue or does work in the background.',
      'You want several copies of a service behind one address.',
    ],
    how: [
      'Open Apps and choose New app.',
      'Give it a name, then the image (for example nginx:1.27-alpine) and the port it listens on.',
      'Turn on Publish to reach it from this computer, or add a domain later for a web address with HTTPS.',
      'Choose Create. The steps Pull, Start, Health and Switch show its progress.',
    ],
    command: 'enclave app create web --image nginx:1.27-alpine --port 80 --publish --wait',
    example: [
      'A shop API: create a database called orders, then an app called api with the image of your API.',
      'Give the app the database password as an environment variable from the secret orders-password, and the database address orders.database.internal.',
      'Add a domain api.localhost routed to api. The API now answers at https://api.localhost and talks to its database by name.',
    ],
    note: [
      'Each copy of an app takes the memory you give it (256 MiB unless you choose).',
      'Settings change through a new deploy; the old version keeps serving until the new one is healthy.',
      'An app with Publish on runs one copy, because a port on this computer can only belong to one.',
    ],
    related: ['domains', 'databases', 'secrets', 'load-balancers'],
  },
  jobs: {
    what: [
      'A job is a command that runs on a schedule: every night, every hour, on Mondays. Each run is kept with its output, so you can see what happened.',
      'A job runs in its own short-lived container, from an image or with the same setup as one of your apps.',
    ],
    when: [
      'Clean up old files or rows every night.',
      'Send a report every Monday morning.',
      'Sync data from another service every hour.',
    ],
    how: [
      'Open Jobs and choose New job.',
      'Give it a name, a schedule (for example 0 2 * * * for 02:00 every day) and the command to run.',
      'Choose an image, or one of your apps to run the command with its image and settings.',
      'Use Run now to try it at once and read its output under Runs.',
    ],
    command: 'enclave job create nightly-report --image alpine:3 --schedule "0 2 * * *" --wait -- echo hello',
    example: [
      'An app called api has a script that removes expired sessions.',
      'Create a job called cleanup with --app api, the schedule 0 * * * * (every hour) and the command node scripts/cleanup.js.',
      'Every hour a run appears under Runs with its exit code and the last lines it printed.',
    ],
    note: [
      'A run that overlaps the previous one is skipped unless you allow overlaps.',
      'Schedules use the machine’s time zone unless you choose another.',
      'Each run keeps its exit code and the last lines it printed.',
    ],
    related: ['apps', 'backups', 'alerts'],
  },
  vms: {
    what: [
      'A VM is a whole computer of its own, with its own operating system, running inside this one. You reach it over SSH and it can join your private networks.',
      'Use it for software that doesn’t fit in a container, or when you want a full Linux machine to work on.',
    ],
    when: [
      'Software that needs a full operating system or its own kernel settings.',
      'A machine to log into and work on, like a small server.',
      'Testing something in isolation from everything else.',
    ],
    how: [
      'Open VMs and choose New VM.',
      'Choose an image (for example debian-12), how many CPUs and how much memory.',
      'Paste your SSH public key, so you can log in.',
      'Choose Create; once it runs, its page shows the SSH command.',
    ],
    command: 'enclave vm create dev --image debian-12 --ssh-key ~/.ssh/id_ed25519.pub --wait',
    example: [
      'A build box: create a VM called builder with 2 CPUs, 4 GiB of memory and a 30 GiB disk.',
      'Log in with the SSH command from its page and install your tools.',
      'Take a snapshot when it’s set up, so you can start new VMs from it later.',
    ],
    note: [
      'VMs need QEMU on this computer; the VMs page says how to install it if it’s missing.',
      'A VM keeps its memory while it runs, even when idle. Stop it when you don’t need it.',
      'Attach disk volumes for data that must outlive the VM.',
    ],
    related: ['volumes', 'snapshots', 'networks', 'firewalls'],
  },
  'load-balancers': {
    what: [
      'A load balancer spreads traffic across several copies of your apps, and stops sending to a copy that fails its health check.',
      'It has one address and port; behind it can be one app with several replicas, or several apps with weights.',
    ],
    when: [
      'An app runs several replicas and you want one address for all of them.',
      'You move traffic from one version of a service to another gradually, with weights.',
      'You need TCP balancing for something that isn’t HTTP.',
    ],
    how: [
      'Open Load balancers and choose New load balancer.',
      'Choose HTTP or TCP and the port it listens on.',
      'Add the apps it sends to, with a weight for each.',
      'Choose Create; its page shows how many targets are healthy.',
    ],
    command: 'enclave lb create front --port 8080 --target api --wait',
    example: [
      'An app api runs 3 replicas. Create a load balancer front on port 8080 with api as its target.',
      'Requests to front spread over the three copies; if one fails its health check, it gets no traffic until it recovers.',
    ],
    note: [
      'Targets must be on the same network as the load balancer.',
      'Set the health check in the load balancer’s form: a target that fails it gets no traffic.',
    ],
    related: ['apps', 'networks', 'domains'],
  },
  databases: {
    what: [
      'A database stores your app’s data and keeps it safe on disk: PostgreSQL, MySQL, MongoDB or ClickHouse.',
      'EnclaveStack creates it with a strong owner password, stored as a secret you can give to your apps.',
    ],
    when: [
      'Your app needs to store users, orders, posts or anything else that must not be lost.',
      'You want analytics over a lot of rows (ClickHouse).',
      'You need a document store (MongoDB).',
    ],
    how: [
      'Open Databases and choose New database.',
      'Give it a name and choose the engine and version (PostgreSQL 16 unless you choose).',
      'Choose Create. Its page shows the address and the secret that holds the password.',
      'Give that secret to your app as an environment variable.',
    ],
    command: 'enclave db create orders --wait',
    example: [
      'Create a database called orders. EnclaveStack also creates the secret orders-password.',
      'Create an app with DATABASE_HOST=orders.database.internal and DB_PASSWORD from the secret orders-password.',
      'Add a backup that copies orders to a bucket every night.',
    ],
    note: [
      'Its data lives on a volume on this machine. Add a backup to keep a copy somewhere else.',
      'The password is never shown in logs or events; reveal it from the secret when you need it.',
      'Deleting a database deletes its data. The console asks you to type its name first.',
    ],
    related: ['backups', 'secrets', 'apps'],
  },
  caches: {
    what: [
      'A cache is a Redis server: very fast storage in memory for things your app reads often or keeps for a short time.',
      'It also writes to disk, so it comes back with its data after a restart.',
    ],
    when: [
      'Sessions of logged-in users.',
      'Counting requests to limit how often someone can call your API.',
      'Keeping the result of a slow query for a few minutes.',
    ],
    how: [
      'Open Caches and choose New cache.',
      'Give it a name and how much memory it may use.',
      'Choose Create; its page shows its address.',
    ],
    command: 'enclave cache create sessions --wait',
    example: [
      'Create a cache called sessions with 256 MiB.',
      'Your app connects to sessions.cache.internal on port 6379 and stores each login there for a day.',
    ],
    note: [
      'When it’s full, the keys used least recently go first.',
      'Use Purge to empty it; its page asks before it does.',
    ],
    related: ['apps', 'backups', 'secrets'],
  },
  buckets: {
    what: [
      'A bucket stores files: uploads, images, videos, backups, archives. Your apps use it through the S3 protocol, which most languages have a library for.',
      'Files can be private or readable by anyone with the link.',
    ],
    when: [
      'Users upload pictures or documents.',
      'You keep backups or exports.',
      'You serve images or videos to visitors.',
    ],
    how: [
      'Open Buckets and choose New bucket.',
      'Give it a name; turn on versioning to keep old copies of files that change.',
      'Choose Create. Its page shows the endpoint and the credentials your app needs.',
    ],
    command: 'enclave bucket create uploads --wait',
    example: [
      'Create a bucket called uploads.',
      'Your app uploads profile pictures to it with an S3 library, using the endpoint and the secret objectstore-credentials.',
      'Put a CDN in front of the bucket to serve the pictures quickly over HTTPS.',
    ],
    note: [
      'Files live on this machine’s disk; watch the free disk on the overview.',
      'Versioning keeps every version of a file, which uses more space.',
    ],
    related: ['cdn', 'backups', 'transcoders'],
  },
  volumes: {
    what: [
      'A volume is storage that outlives what uses it. A disk volume is a hard disk for a VM; a filesystem volume is a folder that apps and jobs keep across deploys.',
    ],
    when: [
      'A VM needs a disk.',
      'An app writes files that must survive a new version, like a folder of uploads.',
      'Several jobs share files.',
    ],
    how: [
      'Open Volumes and choose New volume.',
      'Choose disk (for VMs) or filesystem (for apps and jobs) and its size.',
      'Attach it from the VM, app or job form.',
    ],
    command: 'enclave volume create media --type filesystem --wait',
    example: [
      'Create a filesystem volume called media and mount it at /data/media in your app.',
      'Files written there stay when the app is redeployed or moved.',
    ],
    note: [
      'A disk volume belongs to one VM at a time.',
      'Take a snapshot before a risky change; you can start a new volume from it.',
    ],
    related: ['vms', 'snapshots', 'apps'],
  },
  snapshots: {
    what: [
      'A snapshot is a full copy of a volume or a VM’s disk at one moment, taken while it is in use.',
      'New volumes and VMs can start from a snapshot.',
    ],
    when: [
      'Before an upgrade, so you can go back.',
      'To copy a VM you set up once.',
      'To keep a copy of files from a certain day.',
    ],
    how: [
      'Open the volume or VM and choose Take snapshot, or open Snapshots and choose New snapshot.',
      'Give it a name and choose what to copy.',
      'From the snapshot’s page, choose Create volume or Launch VM to use it.',
    ],
    command: 'enclave snapshot create media-before --from volume/media --wait',
    example: [
      'You set up a VM builder with your tools. Take a snapshot builder-ready.',
      'Each new build box is a Launch VM from builder-ready, ready in seconds with the same tools.',
    ],
    note: [
      'A snapshot takes as much disk as what it copies.',
      'It’s a copy on this machine; for safety elsewhere, use backups to a bucket outside it.',
    ],
    related: ['volumes', 'vms', 'backups'],
  },
  backups: {
    what: [
      'A backup copies a database or a cache to a bucket on a schedule, and keeps the last few copies.',
      'From any copy you can restore: into the same database, or into a new one to look at first.',
    ],
    when: [
      'Every database that holds data you can’t lose.',
      'Before a risky change, with Back up now.',
      'To copy data to another place, like an outside bucket.',
    ],
    how: [
      'Open Backups and choose New backup.',
      'Choose the database or cache, the bucket, a schedule and how many copies to keep.',
      'Use Back up now to try it; each run shows its size.',
      'To restore, open a run and choose Restore.',
    ],
    command: 'enclave backup create orders-nightly --source database/orders --bucket uploads --schedule "0 3 * * *" --keep 7 --wait',
    example: [
      'Create a backup orders-nightly of database orders to a bucket backups at 03:00, keeping 7.',
      'A week later there are seven copies; the oldest goes when a new one arrives.',
      'Someone deletes rows by mistake: restore last night’s copy into a new database, check it, then copy back what you need.',
    ],
    note: [
      'A bucket on this machine doesn’t protect you if the machine is lost. Use an external S3 bucket for that.',
      'Restoring into the same database replaces its data; the console asks you to type the name.',
    ],
    related: ['databases', 'caches', 'buckets'],
  },
  search: {
    what: [
      'A search service is OpenSearch with a password: it finds words across lots of text quickly, and can sum up and count large amounts of data.',
    ],
    when: [
      'A search box over products, articles or documents.',
      'Looking through logs or events.',
      'Dashboards of counts over time.',
    ],
    how: [
      'Open Search and choose New search service.',
      'Give it a name and how much memory it may use.',
      'Choose Create; its page shows the address and the secret that holds the password.',
    ],
    command: 'enclave search create catalog --wait',
    example: [
      'Create a search service catalog. Your shop app sends each product to it when it changes.',
      'The search box on the site asks catalog for “red shoes” and gets the best matches back in milliseconds.',
    ],
    note: [
      'Search keeps the memory you give it whether busy or idle, and needs more than most services.',
      'Its version can’t change after it’s created.',
    ],
    related: ['apps', 'databases'],
  },
  networks: {
    what: [
      'A network is a private space for your resources, with its own range of addresses. Resources in it reach each other by name; resources in different networks can’t reach each other.',
      'Everything you create without choosing a network is on the default network.',
    ],
    when: [
      'Keep a project’s database away from everything else.',
      'Give resources fixed addresses in a range you choose.',
      'Split one network into subnets, for example one for apps and one for data.',
    ],
    how: [
      'Open Networks and choose New network.',
      'Give it a name and an address range, like 10.20.0.0/16.',
      'Add subnets if you want them.',
      'Choose this network in the forms of the resources you create next.',
    ],
    command: 'enclave network create backend --cidr 10.20.0.0/16 --wait',
    example: [
      'Create a network backend. Put a database orders and an app api in it.',
      'api reaches orders.database.internal; an app on the default network can’t reach orders at all.',
    ],
    note: [
      'Ranges can’t overlap with another network or the default one.',
      'Turn off internet access for a network whose resources must not reach outside.',
    ],
    related: ['firewalls', 'load-balancers', 'apps'],
  },
  firewalls: {
    what: [
      'A firewall says who may connect to a resource. Once a firewall names a resource, only what its rules allow gets in.',
      'Rules read like sentences: allow TCP 5432 from app/api.',
    ],
    when: [
      'Only your API may talk to the database.',
      'A VM accepts SSH from one address only.',
      'Something must not reach the internet (deny outgoing traffic).',
    ],
    how: [
      'Open Firewalls and choose New firewall.',
      'Choose the resources it protects.',
      'Add rules: the protocol, the ports and where traffic may come from.',
      'Choose Create; the protected resources show the firewall on their page.',
    ],
    command: 'enclave firewall create db-access --target database/orders --allow "tcp 5432 from app/api" --wait',
    example: [
      'Create a firewall db-access protecting database orders, with the rule tcp 5432 from app/api.',
      'api still connects to orders; any other app trying gets no answer.',
    ],
    note: [
      'Ports published on this computer stay reachable through the machine itself.',
      'With no rule, a protected resource accepts nothing; add the rules you need before you rely on it.',
    ],
    related: ['networks', 'databases', 'vms'],
  },
  domains: {
    what: [
      'A domain is a web address for one of your apps, served over HTTPS. EnclaveStack gets and renews the certificate for you.',
      'Addresses ending in .localhost get a certificate from EnclaveStack itself, for use on this computer.',
    ],
    when: [
      'Put an app on a real address like shop.example.com.',
      'Try an app at https://web.localhost while you build it.',
      'Serve several apps from one machine, each on its own address.',
    ],
    how: [
      'Make the address point to this machine (for a real domain, at your DNS provider, or with a DNS zone here).',
      'Open Domains and choose New domain.',
      'Enter the hostname and choose the app and port.',
      'Choose Create; the certificate appears once it’s issued.',
    ],
    command: 'enclave domain create web-local --hostname web.localhost --app web --wait',
    example: [
      'An app web listens on port 80. Create a domain web-local with the hostname web.localhost routed to web.',
      'Open https://web.localhost. To make your browser trust it, download the root certificate from the domain’s page once.',
    ],
    note: [
      'Real certificates need ports 80 and 443 of this machine reachable from the internet, or a DNS zone for the DNS check.',
      'Plain HTTP redirects to HTTPS unless you turn that off.',
    ],
    related: ['apps', 'zones', 'cdn'],
  },
  zones: {
    what: [
      'A DNS zone is a domain name you own, managed at Cloudflare or Route 53. EnclaveStack keeps the records you add here, and puts them back if they’re changed elsewhere.',
      'A record can point at this machine, and follow it if its address changes.',
    ],
    when: [
      'Point shop.example.com at this machine without logging in to your DNS provider.',
      'Get certificates for wildcard addresses like *.example.com.',
      'Keep all your DNS records in one place with the rest of your setup.',
    ],
    how: [
      'Store an API token for your DNS provider as a secret.',
      'Open DNS and choose New zone: the provider, the domain and that secret.',
      'Open the zone and use Add record for each name you need.',
    ],
    command: 'enclave zone create example --provider cloudflare --zone example.com --credentials cloudflare-token',
    example: [
      'Create a zone example for example.com with your Cloudflare token.',
      'Add a record shop of type A pointing at this machine. When the machine’s public address changes, the record follows.',
    ],
    note: [
      'Give the token only the rights it needs: editing DNS of that one zone.',
      'Records you didn’t create here are left alone.',
    ],
    related: ['domains', 'secrets', 'cdn'],
  },
  cdn: {
    what: [
      'A CDN is a cache with its own web address in front of an app or a bucket. It keeps copies of what visitors ask for, so pages and files come back faster and the app does less work.',
    ],
    when: [
      'Serve images and videos from a bucket over HTTPS.',
      'Speed up a site whose pages rarely change.',
      'Take load off an app during busy times.',
    ],
    how: [
      'Open CDN and choose New CDN.',
      'Enter its hostname and choose the app or bucket it sits in front of.',
      'Choose Create. Use Purge after you change files to clear old copies.',
    ],
    command: 'enclave cdn create media --hostname media.localhost --origin bucket/uploads --wait',
    example: [
      'Create a CDN media at media.localhost in front of the bucket uploads.',
      'Pictures at https://media.localhost/avatars/42.png come from the cache after the first visit.',
    ],
    note: [
      'Purge after you replace a file with the same name, or visitors keep the old one for a while.',
    ],
    related: ['buckets', 'domains', 'apps'],
  },
  queues: {
    what: [
      'A queue holds messages until a worker takes them. If the worker fails, the message comes back and is tried again, so nothing is lost.',
      'Messages that keep failing can go to a dead-letter queue, to look at later.',
    ],
    when: [
      'Send emails, resize images or charge cards in the background.',
      'Smooth out bursts: accept work quickly, do it at your own pace.',
      'Hand work from one service to another.',
    ],
    how: [
      'Open Queues and choose New queue.',
      'Give it a name; set how long a taken message stays hidden and how many tries it gets.',
      'Your app sends messages and your worker receives and deletes them, with the address and password on the queue’s page.',
    ],
    command: 'enclave queue create emails --wait',
    example: [
      'Create a queue emails. When someone signs up, the API sends a message {"to": "…"} to emails.',
      'A worker app receives messages, sends each email and deletes the message. If sending fails, the message comes back after 30 seconds.',
    ],
    note: [
      'Messages are kept for 4 days unless you choose otherwise.',
      'The Messages tab lets you send, receive and delete by hand while you build.',
    ],
    related: ['topics', 'apps', 'jobs'],
  },
  topics: {
    what: [
      'A topic sends each message it gets to every subscription: queues, and web addresses (webhooks) signed with a secret.',
    ],
    when: [
      'One event, several reactions: an order is placed, so email the customer, update stock and tell the warehouse.',
      'Tell another service when something happens, through a webhook.',
    ],
    how: [
      'Open Topics and choose New topic.',
      'Add subscriptions: your queues, or webhook addresses.',
      'Your app publishes messages to the topic; the Deliveries tab shows what arrived where.',
    ],
    command: 'enclave topic create orders --queue emails --wait',
    example: [
      'Create a topic orders with two queues subscribed: emails and stock.',
      'The API publishes each new order once; both queues get a copy, and their workers act on it separately.',
    ],
    note: [
      'Published messages are kept for a day so a subscription that fails can get them again.',
      'Sign webhooks with a secret so the receiver can check they came from you.',
    ],
    related: ['queues', 'apps', 'secrets'],
  },
  email: {
    what: [
      'An email service gives your apps an SMTP server to send through. Every message is kept so you can read it in the console.',
      'Add your email provider as a relay when you want messages actually delivered.',
    ],
    when: [
      'Password resets, receipts and notifications from your app.',
      'Seeing exactly what your app sends while you build it, without sending to real people.',
      'Alert emails from EnclaveStack itself.',
    ],
    how: [
      'Open Email and choose New email service.',
      'Leave the relay empty to only keep messages, or enter your provider’s SMTP host and a secret with its login.',
      'Point your app at the SMTP address and password on the service’s page.',
    ],
    command: 'enclave email create mail --wait',
    example: [
      'Create an email service mail with no relay. Your app sends a password reset email.',
      'Open the Messages tab: the email is there, with its subject, recipients and how it looks.',
      'When you go live, edit mail to relay through your provider; the app doesn’t change.',
    ],
    note: [
      'Without a relay, nothing leaves this machine.',
      'The last 500 messages are kept unless you choose otherwise.',
    ],
    related: ['apps', 'channels', 'secrets'],
  },
  transcoders: {
    what: [
      'A transcoder watches a folder in a bucket and converts every video or audio file that arrives: MP4 in several sizes, HLS for streaming, WebM, MP3 and thumbnails.',
    ],
    when: [
      'Users upload videos that must play well on phones and computers.',
      'You need thumbnails for every video.',
      'You turn recordings into MP3.',
    ],
    how: [
      'Open Transcoders and choose New transcoder.',
      'Choose the bucket folder where files arrive and the folder for results.',
      'Choose the presets you want.',
      'Upload a file to the input folder; the Runs tab shows each conversion.',
    ],
    command: 'enclave transcoder create videos --input uploads/incoming/ --output uploads/ready/ --preset mp4-720p --preset thumbnail --wait',
    example: [
      'Create a transcoder videos watching uploads/incoming/, making mp4-720p and a thumbnail into uploads/ready/.',
      'A user uploads holiday.mov; once it’s converted, uploads/ready/ holds the 720p MP4 and a thumbnail, and the Runs tab shows how long it took.',
    ],
    note: [
      'Converting video uses a lot of CPU; keep the number at once low on a small machine.',
      'A file that takes longer than the time limit (1 hour unless you choose) is stopped.',
    ],
    related: ['buckets', 'cdn', 'jobs'],
  },
  secrets: {
    what: [
      'A secret is a value kept encrypted: a password, an API key, a token. Your apps get it as an environment variable, and it never appears in logs, events or responses.',
    ],
    when: [
      'An API key for a payment provider.',
      'A database password your app needs.',
      'A token EnclaveStack itself uses, like a DNS provider’s.',
    ],
    how: [
      'Open Secrets and choose New secret.',
      'Give it a name and paste the value.',
      'In an app’s form, add an environment variable from this secret.',
    ],
    command: 'enclave secret create stripe-key --value sk_test_123 --wait',
    example: [
      'Create a secret stripe-key. In the app api, add STRIPE_KEY from the secret stripe-key.',
      'When the key changes, update the secret, then use Redeploy on api so it starts with the new value.',
    ],
    note: [
      'Only people who can edit the project can reveal a value, and each reveal is in the audit log.',
      'Secrets EnclaveStack makes for databases and caches are managed for you.',
    ],
    related: ['secret-stores', 'apps', 'databases'],
  },
  'secret-stores': {
    what: [
      'A secret store connects EnclaveStack to where your team already keeps secrets: Vault, 1Password, AWS Secrets Manager, Doppler or Infisical. Secrets are read from there and kept in sync.',
    ],
    when: [
      'Your team manages secrets in one of those already.',
      'Keys rotate there, and your apps should follow.',
    ],
    how: [
      'Store the token or credentials for that store as a secret here.',
      'Open Secret stores and choose New secret store: the provider and that secret.',
      'Use Test to check the connection.',
      'Create secrets that read from the store instead of holding a value.',
    ],
    command: 'enclave secretstore create team-vault --provider vault --address https://vault.example.com --token-secret vault-token',
    example: [
      'Connect the team’s Vault as team-vault. Create a secret stripe-key that reads payments/stripe from it.',
      'When the key is rotated in Vault, the secret takes the new value within 5 minutes; redeploy the apps that use it to pass it on.',
    ],
    note: [
      'Values are read again every 5 minutes unless you choose otherwise; use Refresh now on a secret to read it at once.',
      'If the store can’t be reached, the last value read stays in use.',
    ],
    related: ['secrets', 'apps'],
  },
  stacks: {
    what: [
      'A stack is a product installed from the catalogue, like WordPress or Ghost: an app with its database, storage and domain, created together and changed as one.',
    ],
    when: [
      'Run a blog, a wiki or an analytics tool without wiring it up yourself.',
      'Install the same setup several times, from a template of your own.',
    ],
    how: [
      'Open the Catalogue and choose a product.',
      'Fill in its few questions, such as the domain, and choose Install.',
      'The stack’s page shows its parts and their state; change its settings there.',
    ],
    command: 'enclave stack create blog --template wordpress --param domain=blog.localhost --wait',
    example: [
      'Install WordPress as blog with the domain blog.localhost.',
      'EnclaveStack creates the app blog, the database blog-db, a volume for uploads and the domain. Open https://blog.localhost to finish WordPress’s own setup.',
    ],
    note: [
      'The parts of a stack change through the stack, not one by one.',
      'Deleting a stack deletes all its parts, data included; the console lists them first.',
    ],
    related: ['apps', 'databases', 'domains'],
  },
  monitoring: {
    what: [
      'Monitoring shows graphs of what runs: CPU, memory, requests, queue lengths. This machine has its own dashboard; you can make dashboards of your own.',
    ],
    when: [
      'See whether an app is busy or idle.',
      'Find what uses the memory.',
      'Keep the graphs you watch together on one page.',
    ],
    how: [
      'Open Monitoring and choose This machine for disk, memory, CPU and failures.',
      'Choose New dashboard to make your own: add a graph per metric and the resources to show.',
      'Each resource also has a Metrics tab with its own graphs.',
    ],
    command: 'enclave dashboard create health --widget "title=API CPU,metric=cpu,resources=app/api" --wait',
    example: [
      'Make a dashboard health with two graphs: API CPU and Requests, both for app/api.',
      'Pick 24h to see the day: a spike at 09:00 shows when people start work.',
    ],
    note: [
      'A sample is taken every minute; the first appears within a minute of creating something.',
      'A graph shows at most eight resources.',
    ],
    related: ['alerts', 'channels', 'apps'],
  },
  alerts: {
    what: [
      'An alert watches a metric or a resource and tells your channels when something is wrong: CPU too high, an app restarting again and again, a backup that failed.',
    ],
    when: [
      'Know when the disk is getting full.',
      'Hear about a failed backup the same night.',
      'Catch an app stuck restarting.',
    ],
    how: [
      'Create a channel first, for where the alert should go.',
      'Open Alerts and choose New alert.',
      'Build the rule as a sentence: when this metric of this resource is above this value for this long.',
      'Choose the channels. Use Test to send a test message.',
    ],
    command: 'enclave alert create api-cpu --metric cpu --resource app/api --above 0.9 --channel ops --wait',
    example: [
      'Create an alert api-cpu: when CPU of app/api is above 0.9 cores for 5 minutes, notify ops.',
      'During a busy hour it fires and ops gets a message. When CPU drops, ops hears that it’s back to normal.',
    ],
    note: [
      'An alert fires once and then waits; set Repeat to be reminded while it lasts.',
      'The Incidents tab keeps each time it fired and when it stopped.',
    ],
    related: ['channels', 'monitoring', 'backups'],
  },
  channels: {
    what: [
      'A channel is where alerts go: email, a webhook, Slack, Discord or Telegram.',
    ],
    when: [
      'Send alerts to the team chat.',
      'Email the person on call.',
      'Pass alerts to another system with a webhook.',
    ],
    how: [
      'For chat apps, store the webhook URL or bot token as a secret first.',
      'Open Channels and choose New channel, then the kind and its details.',
      'Use Test to send a test message.',
    ],
    command: 'enclave channel create ops --webhook https://hooks.example.com/enclave --wait',
    example: [
      'Create a channel ops that posts to your team’s chat with its webhook URL in a secret.',
      'Use it in every alert; the Deliveries tab shows each message and whether it arrived.',
    ],
    note: [
      'Email channels send through one of your email services.',
    ],
    related: ['alerts', 'email', 'secrets'],
  },
  'log-exports': {
    what: [
      'A log export sends your apps’ and services’ logs somewhere else as they happen: a bucket, any S3 storage, an HTTP endpoint, OpenTelemetry or Grafana Loki.',
    ],
    when: [
      'Keep logs for longer than this machine does.',
      'Search all your logs in a tool you already use.',
      'Keep a copy of logs for audits.',
    ],
    how: [
      'Open Log exports and choose New log export.',
      'Choose where logs go and, if needed, a secret with the credentials.',
      'Choose which resources to send, or all of the project, and a filter if you want only some lines.',
    ],
    command: 'enclave logexport create archive --bucket uploads/logs --wait',
    example: [
      'Create a log export archive to the bucket logs, folder app-logs.',
      'Every minute a file of the latest lines arrives there, compressed, one JSON object per line.',
    ],
    note: [
      'Secrets never appear in logs, so they never appear in exports.',
      'The page shows how many lines were sent and the last error, if any.',
    ],
    related: ['buckets', 'monitoring', 'secrets'],
  },
};
