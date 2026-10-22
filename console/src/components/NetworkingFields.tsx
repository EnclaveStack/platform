import { Button, Checkbox, Field, FieldError, FieldHint, Fieldset, FormRow, Icons, IconButton, Input, Select } from '@enclavestack/ui';
import { RULES } from '../api/generated';
import type { FirewallRule, LoadBalancerTarget, NetworkSpec, Protocol, Resource } from '../api/types';

// Field groups of the v0.4 forms (L2.5-002 §11): placement, firewall rules, load balancer targets.

interface PlacementProps {
  networks: Resource<'network'>[];
  network: string;
  subnet: string;
  onChange: (network: string, subnet: string) => void;
  errors: Record<string, string>;
  /** Why placement is fixed, e.g. for a job that runs with an app's image. */
  disabledReason?: string;
}

/** Network and subnet pickers; "Default network" first (L2.5-002 §11). */
export function PlacementFields({ networks, network, subnet, onChange, errors, disabledReason }: PlacementProps) {
  const chosen = networks.find((n) => n.name === network);
  const subnets = (chosen?.spec as NetworkSpec | undefined)?.subnets ?? [];
  return (
    <Fieldset legend="Network">
      <FormRow>
        <Field id="f-network" label="Network" error={errors['spec.network']}
          hint={disabledReason ?? (network ? `Answers as <name>.<kind>.internal inside ${network}.` : 'Resources in other networks can’t reach it.')}>
          <Select id="f-network" value={network} disabled={Boolean(disabledReason)} onChange={(e) => onChange(e.target.value, '')}>
            <option value="">Default network</option>
            {networks.map((n) => (
              <option key={n.name} value={n.name}>{n.name} · {(n.spec as NetworkSpec).cidr}</option>
            ))}
          </Select>
        </Field>
        {network && (
          <Field id="f-subnet" label="Subnet" error={errors['spec.subnet']} hint="Empty: the network's first subnet.">
            <Select id="f-subnet" value={subnet} onChange={(e) => onChange(network, e.target.value)}>
              <option value="">First subnet</option>
              {subnets.map((s) => (
                <option key={s.name} value={s.name}>{s.name} · {s.cidr}</option>
              ))}
            </Select>
          </Field>
        )}
      </FormRow>
    </Fieldset>
  );
}

/** Whether a network lets its resources publish a host port. */
export function hasInternet(networks: Resource<'network'>[], name: string): boolean {
  return (networks.find((n) => n.name === name)?.spec as NetworkSpec | undefined)?.internet !== false;
}

export interface RuleRow {
  protocol: Protocol;
  ports: string;
  peers: string;
}

export const ruleRows = (rules: FirewallRule[] | undefined, dir: 'from' | 'to'): RuleRow[] =>
  (rules ?? []).map((r) => ({ protocol: r.protocol ?? 'tcp', ports: (r.ports ?? []).join(', '), peers: ((dir === 'from' ? r.from : r.to) ?? []).join(', ') }));

const split = (s: string) => s.split(/[,\s]+/).map((x) => x.trim()).filter(Boolean);

export function rulesOf(rows: RuleRow[], dir: 'from' | 'to'): FirewallRule[] {
  return rows.map((r) => {
    const rule: FirewallRule = { protocol: r.protocol, [dir]: split(r.peers) };
    const ports = split(r.ports);
    if (ports.length && r.protocol !== 'icmp') rule.ports = ports;
    return rule;
  });
}

/** "Allow tcp 5432 from app/api" — a rule as a sentence (L2.5-002 §11). */
export function ruleSentence(r: FirewallRule, dir: 'from' | 'to'): string {
  const what = r.protocol === 'all' || !r.protocol ? (r.protocol === 'all' ? 'all' : 'tcp') : r.protocol;
  const ports = r.ports?.length ? ` ${r.ports.join(', ')}` : what === 'all' || what === 'icmp' ? '' : ' (all ports)';
  return `Allow ${what}${ports} ${dir} ${((dir === 'from' ? r.from : r.to) ?? []).join(', ')}`;
}

interface RuleEditorProps {
  legend: string;
  dir: 'from' | 'to';
  field: string;
  rows: RuleRow[];
  onChange: (rows: RuleRow[]) => void;
  peers: string[];
  errors: Record<string, string>;
}

/** Rule rows: protocol, ports, and sources or destinations picked from a list or typed as CIDRs. */
export function RuleEditor({ legend, dir, field, rows, onChange, peers, errors }: RuleEditorProps) {
  const update = (i: number, patch: Partial<RuleRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  const listId = `${field}-peers`;
  return (
    <Fieldset legend={legend}>
      {rows.length === 0 && <FieldHint>{dir === 'from' ? 'No rule yet: nothing but EnclaveStack’s own services gets in.' : 'No rule yet: nothing goes out.'}</FieldHint>}
      <datalist id={listId}>
        {[RULES.peerAny, ...peers].map((p) => (
          <option key={p} value={p} />
        ))}
      </datalist>
      {rows.map((row, i) => {
        const f = `${field}[${i}]`;
        const err = errors[`${f}.${dir}`] ?? errors[`${f}.${dir}[0]`] ?? errors[`${f}.ports`] ?? errors[`${f}.ports[0]`] ?? errors[`${f}.protocol`];
        return (
          <FormRow key={i}>
            <Select aria-label={`Rule ${i + 1} protocol`} value={row.protocol} onChange={(e) => update(i, { protocol: e.target.value as Protocol })}>
              {RULES.protocols.map((p) => (
                <option key={p} value={p}>{p}</option>
              ))}
            </Select>
            <Input aria-label={`Rule ${i + 1} ports`} mono placeholder={row.protocol === 'icmp' ? 'no ports' : '5432, 8000-8100'} value={row.ports}
              disabled={row.protocol === 'icmp'} onChange={(e) => update(i, { ports: e.target.value })} spellCheck={false} />
            <Input aria-label={`Rule ${i + 1} ${dir === 'from' ? 'sources' : 'destinations'}`} mono list={listId} placeholder={dir === 'from' ? 'app/api, 10.0.0.0/8' : 'any'}
              value={row.peers} onChange={(e) => update(i, { peers: e.target.value })} spellCheck={false} />
            <IconButton label={`Remove rule ${i + 1}`} icon={<Icons.X />} variant="ghost" onClick={() => onChange(rows.filter((_, j) => j !== i))} />
            {err && <FieldError>{err}</FieldError>}
          </FormRow>
        );
      })}
      <div>
        <Button variant="ghost" size="sm" icon={<Icons.Plus />} onClick={() => onChange([...rows, { protocol: 'tcp', ports: '', peers: '' }])}>Add rule</Button>
      </div>
    </Fieldset>
  );
}

interface TargetChecklistProps {
  candidates: string[];
  selected: string[];
  onChange: (selected: string[]) => void;
  error?: string;
}

/** Resources a firewall protects, as checkboxes. */
export function TargetChecklist({ candidates, selected, onChange, error }: TargetChecklistProps) {
  return (
    <Fieldset legend="Targets">
      {candidates.length === 0 && <FieldHint>Create an app, database, cache, load balancer, VM, search service or email service first.</FieldHint>}
      {candidates.map((c) => (
        <Checkbox key={c} label={c} checked={selected.includes(c)}
          onChange={(e) => onChange(e.target.checked ? [...selected, c] : selected.filter((s) => s !== c))} />
      ))}
      {error && <FieldError>{error}</FieldError>}
    </Fieldset>
  );
}

export interface TargetRow {
  app: string;
  port: string;
  weight: string;
}

export const targetRows = (t: LoadBalancerTarget[] | undefined): TargetRow[] =>
  (t ?? []).map((x) => ({ app: x.app, port: x.port ? String(x.port) : '', weight: String(x.weight ?? RULES.lbWeightDefault) }));

export function targetsOf(rows: TargetRow[]): LoadBalancerTarget[] {
  return rows.map((r) => {
    const t: LoadBalancerTarget = { app: r.app, weight: Number(r.weight) || RULES.lbWeightDefault };
    if (r.port.trim()) t.port = Number(r.port);
    return t;
  });
}

/** Load balancer target rows: app, port, weight. */
export function TargetEditor({ rows, onChange, apps, errors }: { rows: TargetRow[]; onChange: (rows: TargetRow[]) => void; apps: string[]; errors: Record<string, string> }) {
  const update = (i: number, patch: Partial<TargetRow>) => onChange(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));
  return (
    <Fieldset legend="Targets">
      {rows.length === 0 && <FieldHint>Add the apps to send traffic to. They must be in one network.</FieldHint>}
      {rows.map((row, i) => {
        const f = `spec.targets[${i}]`;
        const err = errors[`${f}.app`] ?? errors[`${f}.port`] ?? errors[`${f}.weight`];
        return (
          <FormRow key={i}>
            <Select aria-label={`Target ${i + 1} app`} value={row.app} onChange={(e) => update(i, { app: e.target.value })}>
              <option value="">Choose an app…</option>
              {apps.map((a) => (
                <option key={a} value={a}>{a}</option>
              ))}
            </Select>
            <Input aria-label={`Target ${i + 1} port`} inputMode="numeric" placeholder="app's port" value={row.port} onChange={(e) => update(i, { port: e.target.value })} />
            <Input aria-label={`Target ${i + 1} weight`} inputMode="numeric" value={row.weight} onChange={(e) => update(i, { weight: e.target.value })} />
            <IconButton label={`Remove target ${i + 1}`} icon={<Icons.X />} variant="ghost" onClick={() => onChange(rows.filter((_, j) => j !== i))} />
            {err && <FieldError>{err}</FieldError>}
          </FormRow>
        );
      })}
      {errors['spec.targets'] && <FieldError>{errors['spec.targets']}</FieldError>}
      <div>
        <Button variant="ghost" size="sm" icon={<Icons.Plus />} onClick={() => onChange([...rows, { app: '', port: '', weight: String(RULES.lbWeightDefault) }])}>Add target</Button>
      </div>
    </Fieldset>
  );
}
