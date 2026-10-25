import { Choice, ConfirmDialog, Field, Fieldset, FormRow, Input, Select, Stack, Text, Textarea } from '@enclavestack/ui';
import { useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { RULES } from '../api/generated';
import type { CloudflareAccess, DomainSpec, Gate, GateMode, MachineSummary, Resource, TailscaleMode, TunnelProvider, TunnelSpec } from '../api/types';
import type { FieldErrors } from '../lib/validation';
import { fromRows, KeyValueEditor, ListEditor, toRows, type Row } from './KeyValueEditor';

// Public access in forms (L2-001 CON-76, CON-77; L2.5-002 §21): the tunnel form, the gate, and the
// domain form's Public access fieldset. ResourceForm owns the name, submission and errors.

/** The ways in, as the form's cards and the catalogue's groups show them. */
export const WAYS_IN: { provider: TunnelProvider; entry: string; title: string; description: string; family: 'app' | 'domain' }[] = [
  { provider: 'quick', entry: 'quick-link', family: 'app', title: 'Quick link', description: 'No account, no domain. A random address that changes when it restarts, for demos.' },
  { provider: 'tailscale', entry: 'tailscale', family: 'app', title: 'Tailscale', description: 'Free account. A stable name.ts.net address, private to your own devices or public.' },
  { provider: 'ngrok', entry: 'ngrok', family: 'app', title: 'ngrok', description: 'Free account. One address per account; monthly limits, and a warning page before visitors.' },
  { provider: 'custom', entry: 'custom-tunnel', family: 'app', title: 'Custom', description: 'Any other tunnel service, from its own container image.' },
  { provider: 'cloudflare', entry: 'cloudflare-tunnel', family: 'domain', title: 'Cloudflare', description: "Free. Your domain's DNS must be at Cloudflare." },
  { provider: 'relay', entry: 'own-server', family: 'domain', title: 'Your own server', description: 'A small server you rent. No third party sees your traffic.' },
];

/** The way in a built-in Public access catalogue entry installs (internal/catalogue/builtin). */
export const providerOfEntry = (name: string): TunnelProvider | undefined => WAYS_IN.find((w) => w.entry === name)?.provider;

export const WAY_TITLE = Object.fromEntries(WAYS_IN.map((w) => [w.provider, w.title])) as Record<TunnelProvider, string>;

export function isAppLink(p: TunnelProvider | undefined): boolean {
  return (RULES.appLinkProviders as readonly string[]).includes(p ?? '');
}

/** The gate in force on an app link or a public domain: as given, or the default (GATE-01). */
export function gateOf(spec: { gate?: Gate; provider?: TunnelProvider; mode?: TailscaleMode }): Gate {
  if (spec.gate) return spec.gate;
  return spec.provider === 'tailscale' && (spec.mode ?? 'private') === 'private' ? { mode: 'none' } : { mode: 'signin' };
}

export const GATE_TITLE: Record<GateMode, string> = { signin: 'Sign in', password: 'Password', none: 'Open to everyone' };

/** The gate's state and its three choices, with the confirmation before Open to everyone (GATE-05). */
export function useGate(initial: Gate | undefined) {
  const [mode, setMode] = useState<GateMode>(initial?.mode ?? 'signin');
  const [passwordSecret, setPasswordSecret] = useState(initial?.passwordSecret ?? '');
  const [openPaths, setOpenPaths] = useState<string[]>(initial?.openPaths ?? []);
  const [confirming, setConfirming] = useState(false);

  /** The gate to send; undefined leaves the server's default (signin) when nothing was chosen. */
  function build(): Gate | undefined {
    const paths = openPaths.map((p) => p.trim()).filter(Boolean);
    if (!initial && mode === 'signin' && !paths.length) return undefined;
    return { mode, ...(mode === 'password' ? { passwordSecret } : {}), ...(paths.length ? { openPaths: paths } : {}) };
  }

  function render({ host, secrets, errors }: { host: string; secrets: string[]; errors: FieldErrors }): ReactNode {
    return (
      <>
        <Fieldset legend="Gate">
          <Choice name="gate-mode" value="signin" checked={mode === 'signin'} onChange={() => setMode('signin')}
            title="Sign in" description="People with access to this project sign in with their EnclaveStack account." />
          <Choice name="gate-mode" value="password" checked={mode === 'password'} onChange={() => setMode('password')}
            title="Password" description="Share one user name and password, for example with a client." />
          <Choice name="gate-mode" value="none" checked={mode === 'none'} onChange={() => { if (mode !== 'none') setConfirming(true); }}
            title="Open to everyone" description="No gate: anyone with the address opens it." />
          {errors['spec.gate.mode'] && <Text size="sm" tone="bad">{errors['spec.gate.mode']}</Text>}
          {errors['spec.gate'] && <Text size="sm" tone="bad">{errors['spec.gate']}</Text>}
          {mode === 'password' && (
            <Field id="f-gate-secret" label="Password secret" hint={'A secret holding {"username": "…", "password": "…"}.'} error={errors['spec.gate.passwordSecret']}>
              <Select id="f-gate-secret" value={passwordSecret} onChange={(e) => setPasswordSecret(e.target.value)} aria-invalid={Boolean(errors['spec.gate.passwordSecret'])}>
                <option value="">Choose a secret…</option>
                {secrets.map((n) => <option key={n} value={n}>{n}</option>)}
              </Select>
            </Field>
          )}
        </Fieldset>
        <ListEditor idPrefix="f-open-path" label="Open paths" rows={openPaths} onChange={setOpenPaths} errors={errors} errorField={(i) => `spec.gate.openPaths[${i}]`}
          addLabel="Add path" emptyHint="Paths that skip the gate, for webhooks from other services, like /webhooks/." placeholder="/webhooks/" max={RULES.gateOpenPathsMax} />
        {errors['spec.gate.openPaths'] && <Text size="sm" tone="bad">{errors['spec.gate.openPaths']}</Text>}
        {confirming && (
          <ConfirmDialog title="Open it to everyone?" confirmLabel="Open it to everyone" danger
            body={<p>Anyone on the internet can open <strong>{host || 'it'}</strong>.</p>}
            onConfirm={() => { setMode('none'); setConfirming(false); }} onCancel={() => setConfirming(false)} />
        )}
      </>
    );
  }

  return { build, render };
}

export interface PublicContext {
  live: (k: 'app' | 'secret' | 'tunnel' | 'zone') => Resource[];
  edgeMachines: MachineSummary[];
  editing: boolean;
  errors: FieldErrors;
  onBlur: () => void;
}

const num = (s: string) => (s.trim() === '' ? undefined : Number(s));

/** The tunnel form (CON-76): six cards, then only the fields of the choice. */
export function useTunnelForm(existing: Resource | undefined, initial: { provider?: TunnelProvider; app?: string }) {
  const s = (existing?.kind === 'tunnel' ? existing.spec : {}) as Partial<TunnelSpec>;
  const [provider, setProvider] = useState<TunnelProvider>(s.provider ?? initial.provider ?? 'quick');
  const [credentials, setCredentials] = useState(s.credentialsSecret ?? '');
  const [accountId, setAccountId] = useState(s.accountId ?? '');
  const [machine, setMachine] = useState(s.machine ?? '');
  const [app, setApp] = useState(s.app ?? initial.app ?? '');
  const [port, setPort] = useState(s.port ? String(s.port) : '');
  const [tsMode, setTsMode] = useState<TailscaleMode>(s.mode ?? 'private');
  const [hostname, setHostname] = useState(s.hostname ?? '');
  const [domain, setDomain] = useState(s.domain ?? '');
  const [image, setImage] = useState(s.image ?? '');
  const [command, setCommand] = useState((s.command ?? []).join('\n'));
  const [env, setEnv] = useState<Row[]>(toRows(s.env));
  const [secretEnv, setSecretEnv] = useState<Row[]>(toRows(s.secretEnv));
  const [url, setUrl] = useState(s.url ?? '');
  const gate = useGate(s.gate);
  const gated = isAppLink(provider) && !(provider === 'tailscale' && tsMode === 'private');

  function build(): Record<string, unknown> {
    const out: TunnelSpec = { provider };
    if (provider === 'cloudflare' || provider === 'tailscale' || provider === 'ngrok') out.credentialsSecret = credentials;
    if (provider === 'cloudflare') out.accountId = accountId.trim().toLowerCase();
    if (provider === 'relay') out.machine = machine;
    if (isAppLink(provider)) {
      out.app = app;
      if (port.trim()) out.port = num(port);
    }
    if (provider === 'tailscale') {
      out.mode = tsMode;
      if (hostname.trim()) out.hostname = hostname.trim().toLowerCase();
    }
    if (provider === 'ngrok' && domain.trim()) out.domain = domain.trim().toLowerCase();
    if (provider === 'custom') {
      out.image = image.trim();
      const cmd = command.split('\n').map((x) => x.trim()).filter(Boolean);
      if (cmd.length) out.command = cmd;
      const e = fromRows(env);
      const se = fromRows(secretEnv);
      if (e) out.env = e;
      if (se) out.secretEnv = se;
      if (url.trim()) out.url = url.trim();
    }
    if (gated) {
      const g = gate.build();
      if (g) out.gate = g;
    }
    return out as unknown as Record<string, unknown>;
  }

  function render({ live, edgeMachines, editing, errors, onBlur }: PublicContext): ReactNode {
    const err = (f: string) => errors[f];
    const secrets = live('secret').map((r) => r.name);
    const secretSelect = (label: string, hint: ReactNode) => (
      <Field id="f-tun-creds" label={label} hint={hint} error={err('spec.credentialsSecret')}>
        <Select id="f-tun-creds" value={credentials} onChange={(e) => setCredentials(e.target.value)} aria-invalid={Boolean(err('spec.credentialsSecret'))}>
          <option value="">Choose a secret…</option>
          {secrets.map((n) => <option key={n} value={n}>{n}</option>)}
        </Select>
      </Field>
    );
    const newSecret = <> <Link to="/secrets?new=1">New secret</Link></>;
    const cards = (family: 'app' | 'domain', legend: string) => (
      <Fieldset legend={legend}>
        {WAYS_IN.filter((w) => w.family === family).map((w) => (
          <Choice key={w.provider} name="tunnel-provider" value={w.provider} checked={provider === w.provider} disabled={editing && provider !== w.provider}
            onChange={() => setProvider(w.provider)} title={w.title} description={w.description} />
        ))}
      </Fieldset>
    );
    return (
      <>
        {cards('app', 'An address for one app')}
        {cards('domain', 'Your own domain')}
        {editing && <Text size="sm" tone="muted">The way in can't change. Create a new tunnel instead.</Text>}
        {err('spec.provider') && <Text size="sm" tone="bad">{err('spec.provider')}</Text>}

        {provider === 'cloudflare' && (
          <FormRow>
            {secretSelect('API token', <>A secret holding a Cloudflare API token with Account › Cloudflare Tunnel › Edit and Zone › DNS › Edit (and Access › Apps and Policies › Edit for Cloudflare Access). Make one at <a href="https://dash.cloudflare.com/profile/api-tokens" target="_blank" rel="noreferrer">dash.cloudflare.com</a>.{newSecret}</>)}
            <Field id="f-tun-account" label="Account ID" hint="On your domain's overview page at Cloudflare: 32 letters and digits." error={err('spec.accountId')}>
              <Input id="f-tun-account" mono value={accountId} onChange={(e) => setAccountId(e.target.value)} onBlur={onBlur} spellCheck={false} aria-invalid={Boolean(err('spec.accountId'))} />
            </Field>
          </FormRow>
        )}
        {provider === 'relay' && (edgeMachines.length || editing ? (
          <Field id="f-tun-machine" label="Edge machine" hint={editing ? "The edge machine can't change." : 'The server with a public address the relay runs on.'} error={err('spec.machine')}>
            <Select id="f-tun-machine" value={machine} onChange={(e) => setMachine(e.target.value)} disabled={editing} aria-invalid={Boolean(err('spec.machine'))}>
              <option value="">Choose an edge machine…</option>
              {edgeMachines.map((m) => <option key={m.name} value={m.name}>{m.name}{m.address ? ` (${m.address})` : ''}</option>)}
            </Select>
          </Field>
        ) : (
          <Text tone="muted">Add an edge machine first: <Link to="/machines?edge=1">Machines › Add an edge machine</Link>. {err('spec.machine')}</Text>
        ))}
        {provider === 'tailscale' && (
          <>
            {secretSelect('Auth key', <>A secret holding a reusable auth key, from the Tailscale admin console › Settings › Keys.{newSecret}</>)}
            <Fieldset legend="Who can open it">
              <Choice name="ts-mode" value="private" checked={tsMode === 'private'} onChange={() => setTsMode('private')}
                title="Private" description="Only devices in your tailnet. No gate is needed." />
              <Choice name="ts-mode" value="public" checked={tsMode === 'public'} onChange={() => setTsMode('public')}
                title="Public" description="Anyone on the internet, with Funnel, behind the gate." />
            </Fieldset>
            <Field id="f-tun-hostname" label="Device name" hint="Its name in your tailnet, the first part of the address. Empty: <project>-<app>." error={err('spec.hostname')}>
              <Input id="f-tun-hostname" mono value={hostname} onChange={(e) => setHostname(e.target.value)} onBlur={onBlur} spellCheck={false} />
            </Field>
          </>
        )}
        {provider === 'ngrok' && (
          <FormRow>
            {secretSelect('Authtoken', <>A secret holding your Authtoken, from the ngrok dashboard.{newSecret}</>)}
            <Field id="f-tun-domain" label="Reserved domain" hint="Optional, on paid plans: a domain reserved in your account. Empty: the account's free address." error={err('spec.domain')}>
              <Input id="f-tun-domain" mono value={domain} onChange={(e) => setDomain(e.target.value)} onBlur={onBlur} placeholder="stage.ngrok.app" spellCheck={false} />
            </Field>
          </FormRow>
        )}
        {provider === 'custom' && (
          <Stack gap={4}>
            <Text size="sm" tone="muted">The connector runs from its own image. Send traffic to ${RULES.upstreamEnv}: EnclaveStack sets it to the gate in front of your app.</Text>
            <Field id="f-tun-image" label="Image" hint="The connector's image, like ghcr.io/acme/tunnel:1." error={err('spec.image')}>
              <Input id="f-tun-image" mono value={image} onChange={(e) => setImage(e.target.value)} onBlur={onBlur} spellCheck={false} aria-invalid={Boolean(err('spec.image'))} />
            </Field>
            <Field id="f-tun-command" label="Command" hint={`Optional. One argument per line; $${RULES.upstreamEnv} is replaced by the address to send traffic to.`} error={err('spec.command')}>
              <Textarea id="f-tun-command" mono rows={3} value={command} onChange={(e) => setCommand(e.target.value)} spellCheck={false} />
            </Field>
            <KeyValueEditor idPrefix="f-tun-env" label="Environment variables" rows={env} onChange={setEnv} errors={errors} errorField={(k) => `spec.env.${k}`}
              addLabel="Add variable" emptyHint="Plain values the connector receives." />
            <KeyValueEditor idPrefix="f-tun-senv" label="Secret environment variables" rows={secretEnv} onChange={setSecretEnv} options={secrets} errors={errors}
              errorField={(k) => `spec.secretEnv.${k}`} addLabel="Add secret variable" emptyHint="Tokens the connector needs, taken from secrets." />
            <Field id="f-tun-url" label="Public address" hint="Optional: the https:// address the service gives, to show here." error={err('spec.url')}>
              <Input id="f-tun-url" mono value={url} onChange={(e) => setUrl(e.target.value)} onBlur={onBlur} placeholder="https://stage.example.net" spellCheck={false} />
            </Field>
          </Stack>
        )}
        {isAppLink(provider) && (
          <FormRow>
            <Field id="f-tun-app" label="App" hint="The app to make public." error={err('spec.app')}>
              <Select id="f-tun-app" value={app} onChange={(e) => setApp(e.target.value)} aria-invalid={Boolean(err('spec.app'))}>
                <option value="">Choose an app…</option>
                {live('app').map((a) => <option key={a.name} value={a.name}>{a.name}</option>)}
              </Select>
            </Field>
            <Field id="f-tun-port" label="App port" hint="Empty: the app's port." error={err('spec.port')}>
              <Input id="f-tun-port" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} onBlur={onBlur} />
            </Field>
          </FormRow>
        )}
        {gated && gate.render({ host: app ? `the address of ${app}` : '', secrets, errors })}
        {!isAppLink(provider) && <Text size="sm" tone="muted">Then add public domains through it: Domains › New domain › From the internet. Each domain has its own gate.</Text>}
      </>
    );
  }

  return { build, render };
}

/** The domain form's Public access fieldset (CON-77). */
export function useDomainPublic(existing: Resource | undefined, initialTunnel?: string) {
  const d = (existing?.kind === 'domain' ? existing.spec : {}) as Partial<DomainSpec>;
  const [where, setWhere] = useState<'local' | 'internet'>(d.tunnel || initialTunnel ? 'internet' : 'local');
  const [tunnel, setTunnel] = useState(d.tunnel ?? initialTunnel ?? '');
  const [emails, setEmails] = useState<string[]>(d.cloudflareAccess?.emails ?? []);
  const [emailDomains, setEmailDomains] = useState<string[]>(d.cloudflareAccess?.emailDomains ?? []);
  const gate = useGate(d.gate);
  const isPublic = where === 'internet';

  /** Adds the public fields to a domain spec; a local domain clears them. */
  function apply(spec: DomainSpec, live: PublicContext['live']): DomainSpec {
    if (!isPublic) return spec;
    const out: DomainSpec = { ...spec, tunnel, tls: 'auto' };
    const g = gate.build();
    if (g) out.gate = g;
    const provider = (live('tunnel').find((t) => t.name === tunnel)?.spec as TunnelSpec | undefined)?.provider;
    const e = emails.map((x) => x.trim()).filter(Boolean);
    const ed = emailDomains.map((x) => x.trim()).filter(Boolean);
    if (provider === 'cloudflare' && (e.length || ed.length)) {
      const access: CloudflareAccess = {};
      if (e.length) access.emails = e;
      if (ed.length) access.emailDomains = ed;
      out.cloudflareAccess = access;
    }
    return out;
  }

  function render({ live, errors, host, zone, zoneField }: PublicContext & { host: string; zone: string; zoneField: ReactNode }): ReactNode {
    const tunnels = live('tunnel').filter((t) => !isAppLink((t.spec as TunnelSpec).provider));
    const provider = (tunnels.find((t) => t.name === tunnel)?.spec as TunnelSpec | undefined)?.provider;
    const secrets = live('secret').map((r) => r.name);
    return (
      <>
        <Fieldset legend="Public access">
          <Choice name="domain-where" value="local" checked={!isPublic} onChange={() => setWhere('local')}
            title="Only on this machine" description="Served by this machine's own ports: on your network, or where its ports 80 and 443 are reachable." />
          <Choice name="domain-where" value="internet" checked={isPublic} onChange={() => setWhere('internet')}
            title="From the internet" description="Through a Cloudflare or own-server tunnel, with nothing opened on your router." />
        </Fieldset>
        {isPublic && (
          <>
            {tunnels.length ? (
              <Field id="f-domain-tunnel" label="Tunnel" hint="The way in this domain is served through." error={errors['spec.tunnel']}>
                <Select id="f-domain-tunnel" value={tunnel} onChange={(e) => setTunnel(e.target.value)} aria-invalid={Boolean(errors['spec.tunnel'])}>
                  <option value="">Choose a tunnel…</option>
                  {tunnels.map((t) => <option key={t.name} value={t.name}>{t.name} ({WAY_TITLE[(t.spec as TunnelSpec).provider]})</option>)}
                </Select>
              </Field>
            ) : (
              <Text tone="muted">No tunnel yet: <Link to="/tunnels?new=1">add a Cloudflare or own-server tunnel</Link> first. {errors['spec.tunnel']}</Text>
            )}
            {zoneField}
            <Text size="sm" tone="muted">
              {provider === 'cloudflare' ? `Required for Cloudflare: the zone of ${host || 'this hostname'}, at Cloudflare. EnclaveStack writes the record and Cloudflare serves the certificate.`
                : zone ? 'With a zone, EnclaveStack writes the DNS record.' : 'Without a zone, you point the DNS record yourself: the domain shows the record to add.'}
            </Text>
            {gate.render({ host, secrets, errors })}
            {provider === 'cloudflare' && (
              <Fieldset legend="Cloudflare Access">
                <Text size="sm" tone="muted">Optional. Cloudflare checks these people with a one-time code before EnclaveStack's gate.</Text>
                <ListEditor idPrefix="f-access-email" label="Emails" rows={emails} onChange={setEmails} errors={errors} errorField={(i) => `spec.cloudflareAccess.emails[${i}]`}
                  addLabel="Add email" emptyHint="Addresses Cloudflare lets in." placeholder="sunny@example.com" max={RULES.accessEmailsMax} />
                <ListEditor idPrefix="f-access-domain" label="Email domains" rows={emailDomains} onChange={setEmailDomains} errors={errors}
                  errorField={(i) => `spec.cloudflareAccess.emailDomains[${i}]`} addLabel="Add email domain" emptyHint="Everyone with an address at these domains." placeholder="example.com"
                  max={RULES.accessEmailDomainsMax} />
                {errors['spec.cloudflareAccess'] && <Text size="sm" tone="bad">{errors['spec.cloudflareAccess']}</Text>}
              </Fieldset>
            )}
          </>
        )}
      </>
    );
  }

  return { isPublic, apply, render };
}
