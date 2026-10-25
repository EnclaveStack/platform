import { Alert, Button, Card, Page, PageHead, Table, Text } from '@enclavestack/ui';
import { useState } from 'react';
import { useApi } from '../api/context';
import type { AuditEntry } from '../api/types';
import { useLoad } from '../lib/useLoad';

// The audit log (CON-53, IAM-09): who signed in, invited, changed roles, made tokens and changed
// projects, newest first.
export function AuditPage() {
  const api = useApi();
  const first = useLoad('audit', () => api.listAudit());
  const [more, setMore] = useState<AuditEntry[]>([]);
  const [cursor, setCursor] = useState<string | null | undefined>(undefined);
  const [busy, setBusy] = useState(false);
  const next = cursor === undefined ? first.data?.nextCursor : cursor;
  const rows = [...(first.data?.items ?? []), ...more];

  async function loadMore() {
    if (!next) return;
    setBusy(true);
    try {
      const page = await api.listAudit(next);
      setMore((m) => [...m, ...page.items]);
      setCursor(page.nextCursor);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Page>
      <PageHead title="Audit log" lede="What people and tokens did to this machine: sign-ins, invitations, roles, tokens and projects. Changes to resources are in each resource's events." />
      <Card title="Entries">
        {first.error && <Alert>{first.error.message}</Alert>}
        <Table>
          <thead>
            <tr><th scope="col">Time</th><th scope="col">Person</th><th scope="col">Action</th><th scope="col">Target</th><th scope="col">Detail</th><th scope="col">Address</th></tr>
          </thead>
          <tbody>
            {first.data && rows.length === 0 && <tr><td colSpan={6}><Text tone="subtle">Nothing yet.</Text></td></tr>}
            {rows.map((e) => (
              <tr key={e.id}>
                <td><Text tone="muted">{new Date(e.at).toLocaleString()}</Text></td>
                <td><Text>{e.actor || '—'}</Text></td>
                <td><Text mono size="sm">{e.action}</Text></td>
                <td><Text mono size="sm">{e.target}</Text></td>
                <td><Text tone="muted">{e.detail}</Text></td>
                <td><Text mono size="sm" tone="subtle">{e.clientIp}</Text></td>
              </tr>
            ))}
          </tbody>
        </Table>
        {next && <div><Button onClick={() => void loadMore()} disabled={busy}>{busy ? 'Loading…' : 'Load more'}</Button></div>}
      </Card>
    </Page>
  );
}
