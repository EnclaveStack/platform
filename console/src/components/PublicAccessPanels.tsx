import { Button, buttonClass, Card, Chip, CodeBlock, DetailList, Drawer, Icons, Inline, Stack, Text, useToast } from '@enclavestack/ui';
import { useEffect, useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { useApi } from '../api/context';
import type { DomainSpec, Resource, TunnelSpec } from '../api/types';
import { withHelp } from '../lib/details';
import { useLoad } from '../lib/useLoad';
import { ResourceForm } from '../routes/ResourceForm';
import { GATE_TITLE, gateOf, isAppLink, WAY_TITLE, WAYS_IN } from './PublicAccessFields';
import { StatusBadge } from './StatusBadge';

// Public access on resource pages (L2-001 CON-76 – CON-80, L2.5-002 §21): a tunnel's address and what it
// serves, a public domain's record and gate, and Make public with Public addresses on an app.

const RECORD_STATE: Record<string, string> = {
  created: 'Created', adopted: 'Already there', conflict: 'Different value — fix it at your DNS provider', manual: 'Point it yourself',
};

/** One address with Copy and Open. */
function AddressRow({ url, large }: { url: string; large?: boolean }) {
  const toast = useToast();
  return (
    <Inline justify="between" wrap>
      <Text mono size={large ? 'lg' : 'sm'} weight={large ? 'semibold' : undefined}>{url}</Text>
      <Inline gap={2}>
        <Button size="sm" icon={<Icons.Copy />} onClick={() => { void navigator.clipboard?.writeText(url); toast('Copied'); }}>Copy</Button>
        <a href={url} target="_blank" rel="noreferrer" className={buttonClass({ size: 'sm' })}><Icons.ExternalLink /> Open</a>
      </Inline>
    </Inline>
  );
}

/** The connection, as the tunnel's header shows it. */
export function connectionChip(r: Resource) {
  if (r.kind !== 'tunnel' || r.status.phase !== 'ready') return null;
  return r.outputs.connection === 'reconnecting' ? <Chip tone="warn">Reconnecting</Chip> : <Chip tone="ok">Connected</Chip>;
}

/** A tunnel's page: an app link's address, or the public domains a domain tunnel serves (CON-76). */
export function TunnelCards({ r }: { r: Resource<'tunnel'> }) {
  const api = useApi();
  const toast = useToast();
  const t = r.spec;
  const url = typeof r.outputs.url === 'string' ? r.outputs.url : '';
  // When a quick link restarts its address changes: say so (TUN-04).
  const last = useRef(url);
  useEffect(() => {
    if (last.current && url && last.current !== url) toast(`${r.name} has a new address`);
    last.current = url;
  }, [url, r.name, toast]);
  const domains = useLoad(`tunnel-domains:${r.id}:${r.updatedAt}`, () => (isAppLink(t.provider) ? Promise.resolve([]) : api.listResources('domain', r.project)));
  const serves = (domains.data ?? []).filter((d) => (d.spec as DomainSpec).tunnel === r.name && d.status.phase !== 'deleting');
  if (isAppLink(t.provider)) {
    const gate = gateOf(t);
    return (
      <Card as="section" title="Public address">
        <Stack gap={3}>
          {url ? <AddressRow url={url} large /> : <Text tone="muted">{r.status.phase === 'ready' ? 'The service gives no address to show. Look in the connector’s own dashboard.' : 'The address appears once the connector is connected.'}</Text>}
          {t.provider === 'quick' && <Text size="sm" tone="muted">Temporary: this address changes when the link restarts, and handles at most 200 requests at once.</Text>}
          {t.provider === 'tailscale' && <Text size="sm" tone="muted">{t.mode === 'public' ? 'Public on the internet, with Funnel.' : 'Private: only your tailnet’s devices.'}</Text>}
          {t.provider === 'ngrok' && <Text size="sm" tone="muted">On ngrok’s free plan: about 1 GB and 20,000 requests a month, and ngrok shows visitors a warning page first.</Text>}
          <Text size="sm" tone={gate.mode === 'none' ? 'warn' : 'muted'}>
            Gate: {GATE_TITLE[gate.mode]}{gate.mode === 'none' && t.provider === 'tailscale' && t.mode !== 'public' ? ' — your tailnet is the gate' : ''}{gate.openPaths?.length ? ` · open paths ${gate.openPaths.join(', ')}` : ''}
          </Text>
          {t.app && <Text size="sm" tone="muted">Reaches the app <Link to={`/apps?project=${encodeURIComponent(r.project)}`}>{t.app}</Link>{t.port ? ` on port ${t.port}` : ''}.</Text>}
        </Stack>
      </Card>
    );
  }
  return (
    <Card as="section" title="Serves" action={<Link to="/domains?new=1" className={buttonClass({ size: 'sm' })}><Icons.Plus /> Add a public domain</Link>}>
      {serves.length === 0 ? (
        <Text tone="muted">No public domains yet. Add a domain, choose From the internet and this tunnel.</Text>
      ) : (
        <Stack gap={2}>
          {serves.map((d) => {
            const g = (d.spec as DomainSpec).gate?.mode ?? 'signin';
            return (
              <Inline key={d.id} justify="between" wrap>
                <Link to={`/domains/${encodeURIComponent(d.id)}`}><Text mono size="sm">{(d.spec as DomainSpec).hostname}</Text></Link>
                <Inline gap={2}><Chip tone={g === 'none' ? 'warn' : undefined}>{GATE_TITLE[g]}</Chip><StatusBadge phase={d.status.phase} /></Inline>
              </Inline>
            );
          })}
        </Stack>
      )}
      {t.provider === 'relay' && typeof r.outputs.publicAddress === 'string' && r.outputs.publicAddress && (
        <Text size="sm" tone="muted">Without a zone, point each domain’s A record at {r.outputs.publicAddress}.</Text>
      )}
    </Card>
  );
}

/** A public domain's card: tunnel, DNS record, certificate, gate and Access (CON-77). */
export function PublicDomainCard({ r }: { r: Resource }) {
  const d = r.spec as DomainSpec;
  const url = typeof r.outputs.url === 'string' ? r.outputs.url : '';
  const rec = (r.outputs as Record<string, unknown>).dnsRecord as { type?: string; name?: string; value?: string; state?: string } | undefined;
  const gate = d.gate ?? { mode: 'signin' as const };
  const items = [
    { label: 'Tunnel', value: <Link to={`/tunnels?project=${encodeURIComponent(r.project)}`}>{d.tunnel}</Link> },
    ...(rec ? [{ label: 'DNS record', value: `${rec.type ?? ''} ${rec.name ?? ''} → ${rec.value ?? ''} · ${RECORD_STATE[rec.state ?? ''] ?? rec.state ?? ''}` }] : []),
    { label: 'Certificate', value: r.outputs.certificateState === 'edge' ? 'Served by Cloudflare' : r.outputs.certificateExpiresAt ? `EnclaveStack’s, until ${new Date(String(r.outputs.certificateExpiresAt)).toLocaleDateString()}` : 'Waiting' },
    { label: 'Gate', value: <Text tone={gate.mode === 'none' ? 'warn' : undefined}>{GATE_TITLE[gate.mode]}{gate.mode === 'password' && gate.passwordSecret ? ` (${gate.passwordSecret})` : ''}</Text> },
    ...(gate.openPaths?.length ? [{ label: 'Open paths', value: gate.openPaths.join(', ') }] : []),
    ...(d.cloudflareAccess ? [{ label: 'Cloudflare Access', value: [...(d.cloudflareAccess.emails ?? []), ...(d.cloudflareAccess.emailDomains ?? []).map((x) => `@${x}`)].join(', ') }] : []),
  ];
  return (
    <>
      {url && r.status.phase === 'ready' && <Card padding="tight"><AddressRow url={url} /></Card>}
      <Card as="section" title="Public access">
        <Stack gap={3}>
          <DetailList items={withHelp(items)} />
          {rec && (rec.state === 'manual' || rec.state === 'conflict') && (
            <>
              <Text size="sm" tone="muted">Add this record at your DNS provider. Visitors reach the domain once it resolves.</Text>
              <CodeBlock label="DNS record" code={`${rec.name ?? ''}.  ${rec.type ?? ''}  ${rec.value ?? ''}`} />
            </>
          )}
        </Stack>
      </Card>
    </>
  );
}

/** An app's public addresses: its app links and the public domains that serve it (CON-80). */
export function PublicAddressesCard({ app }: { app: Resource }) {
  const api = useApi();
  const all = useLoad(`public-of:${app.id}:${app.updatedAt}`, () => api.listResources(undefined, app.project));
  const links = (all.data ?? []).filter((x) => x.kind === 'tunnel' && (x.spec as TunnelSpec).app === app.name && x.status.phase !== 'deleting');
  const domains = (all.data ?? []).filter((x) => x.kind === 'domain' && (x.spec as DomainSpec).app === app.name && (x.spec as DomainSpec).tunnel && x.status.phase !== 'deleting');
  if (!links.length && !domains.length) return null;
  return (
    <Card as="section" title="Public addresses">
      <Stack gap={3}>
        {links.map((x) => {
          const url = typeof x.outputs.url === 'string' ? x.outputs.url : '';
          const t = x.spec as TunnelSpec;
          return (
            <Stack key={x.id} gap={1}>
              {url ? <AddressRow url={url} /> : <Text tone="muted" size="sm">Waiting for its address…</Text>}
              <Text size="xs" tone="subtle"><Link to={`/tunnels/${encodeURIComponent(x.id)}`}>{x.name}</Link> · {WAY_TITLE[t.provider]} · gate {GATE_TITLE[gateOf(t).mode]}</Text>
            </Stack>
          );
        })}
        {domains.map((x) => {
          const d = x.spec as DomainSpec;
          return (
            <Stack key={x.id} gap={1}>
              <AddressRow url={`https://${d.hostname}`} />
              <Text size="xs" tone="subtle"><Link to={`/domains/${encodeURIComponent(x.id)}`}>{x.name}</Link> · through {d.tunnel} · gate {GATE_TITLE[d.gate?.mode ?? 'signin']}</Text>
            </Stack>
          );
        })}
      </Stack>
    </Card>
  );
}

/** Make public on an app (CON-80): the installed ways in, an app link for this app, or a public domain. */
export function MakePublic({ app, onClose }: { app: Resource; onClose: () => void }) {
  const api = useApi();
  const toast = useToast();
  const navigate = useNavigate();
  const tunnels = useLoad(`make-public:${app.project}`, () => api.listResources('tunnel', app.project));
  const [form, setForm] = useState<{ kind: 'tunnel'; provider: TunnelSpec['provider'] } | { kind: 'domain'; tunnel: string } | null>(null);
  const installed = (tunnels.data ?? []).filter((t) => t.status.phase !== 'deleting');
  // A quick link needs nothing installed; other app links are offered once one exists (its account is set up).
  const appLinks = WAYS_IN.filter((w) => w.family === 'app' && (w.provider === 'quick' || installed.some((t) => (t.spec as TunnelSpec).provider === w.provider)));
  const domainTunnels = installed.filter((t) => !isAppLink((t.spec as TunnelSpec).provider));
  if (form) {
    return (
      <Drawer title={form.kind === 'tunnel' ? `Make ${app.name} public` : `Public domain for ${app.name}`} onClose={onClose}>
        <ResourceForm kind={form.kind} initialApp={app.name} initialProvider={form.kind === 'tunnel' ? form.provider : undefined} initialTunnel={form.kind === 'domain' ? form.tunnel : undefined}
          onDone={(created) => {
            onClose();
            toast(`Creating ${created.kind} ${created.name}`);
            navigate(`/${created.kind === 'tunnel' ? 'tunnels' : 'domains'}/${encodeURIComponent(created.id)}`);
          }} />
      </Drawer>
    );
  }
  return (
    <Drawer title={`Make ${app.name} public`} onClose={onClose}>
      <Stack gap={4}>
        <Text tone="muted">Choose how people on the internet reach {app.name}. Nothing on your router is opened.</Text>
        <Card title="An address for this app">
          <Stack gap={2}>
            {appLinks.map((w) => (
              <Inline key={w.provider} justify="between" wrap>
                <Stack gap={0}><Text weight="semibold">{w.title}</Text><Text size="sm" tone="muted">{w.description}</Text></Stack>
                <Button size="sm" onClick={() => setForm({ kind: 'tunnel', provider: w.provider })}>Use {w.title}</Button>
              </Inline>
            ))}
          </Stack>
        </Card>
        <Card title="Your own domain">
          {domainTunnels.length ? (
            <Stack gap={2}>
              {domainTunnels.map((t) => (
                <Inline key={t.id} justify="between" wrap>
                  <Inline gap={2}><Text weight="semibold">{t.name}</Text><Chip>{WAY_TITLE[(t.spec as TunnelSpec).provider]}</Chip><StatusBadge phase={t.status.phase} /></Inline>
                  <Button size="sm" onClick={() => setForm({ kind: 'domain', tunnel: t.name })}>Add a domain through {t.name}</Button>
                </Inline>
              ))}
            </Stack>
          ) : <Text tone="muted">No Cloudflare or own-server tunnel yet.</Text>}
        </Card>
        <div><Link to="/catalogue?category=public-access" className={buttonClass({ variant: 'outline' })}><Icons.Plus /> Install another way in</Link></div>
      </Stack>
    </Drawer>
  );
}
