import { createHttpApi } from './http';
import { createMockApi, type MockOptions } from './mock';
import type {
  AuditEntry,
  CreatedToken,
  CreateTokenInput,
  DeletedMessages,
  DeliveryStats,
  Email,
  EmailList,
  Event,
  Incident,
  Invitation,
  InviteInfo,
  InviteUserInput,
  Kind,
  LogLine,
  Machine,
  MachineSettings,
  MessageList,
  MetricData,
  NotificationRecord,
  Project,
  ReceivedMessage,
  Resource,
  RestoreRequest,
  Run,
  RunDetail,
  SendEmail,
  SendMessage,
  SentMessage,
  Session,
  SetupInput,
  SpecByKind,
  TestResult,
  Token,
  UpdateUserInput,
  UserSummary,
} from './types';

/**
 * The only way the console reaches the control plane (ADR-002, ADR-003). Components get it from
 * `useApi()` and never call fetch themselves. Two adapters implement it: `http` (L3-002) and `mock`.
 */
export interface EnclaveApi {
  getSetupRequired(): Promise<boolean>;
  setup(input: SetupInput): Promise<Session>;
  signIn(email: string, password: string): Promise<Session>;
  getSession(): Promise<Session | null>;
  signOut(): Promise<void>;

  listTokens(): Promise<Token[]>;
  createToken(input: CreateTokenInput): Promise<CreatedToken>;
  revokeToken(id: string): Promise<void>;

  changePassword(currentPassword: string, newPassword: string): Promise<void>;
  /** Who an invite or reset link is for (404 NOT_FOUND once used or expired). */
  getInvite(code: string): Promise<InviteInfo>;
  /** Sets the password from a link and signs in. */
  acceptInvite(code: string, password: string): Promise<Session>;

  // v0.7 projects and people (L3-002 §10.1 – §10.3).
  listProjects(): Promise<Project[]>;
  createProject(name: string, description: string): Promise<Project>;
  updateProject(name: string, description: string): Promise<Project>;
  /** 409 PROJECT_NOT_EMPTY lists what's left in its details. */
  deleteProject(name: string): Promise<void>;
  listUsers(): Promise<UserSummary[]>;
  inviteUser(input: InviteUserInput): Promise<Invitation>;
  updateUser(id: string, input: UpdateUserInput): Promise<UserSummary>;
  removeUser(id: string): Promise<void>;
  /** A new one-time link: an invite again, or a password reset. */
  resetUser(id: string): Promise<Invitation>;
  listAudit(cursor?: string): Promise<{ items: AuditEntry[]; nextCursor: string | null }>;

  getMachine(): Promise<Machine>;
  updateMachine(settings: MachineSettings): Promise<Machine>;
  /** The internal CA's PEM root certificate (404 NOT_FOUND before the first internal-TLS domain). */
  getRootCA(): Promise<string>;
  /** Without a project: every project the caller can see. */
  listResources(kind?: Kind, project?: string): Promise<Resource[]>;
  getResource(id: string): Promise<Resource>;
  /** Without a project: the default project. */
  createResource<K extends Kind>(kind: K, name: string, spec: SpecByKind[K], project?: string): Promise<Resource>;
  /** Fields present in `patch` replace the stored ones; `null` clears a field. */
  updateResource(id: string, patch: Record<string, unknown>, ifMatch: number): Promise<Resource>;
  deleteResource(id: string, ifMatch?: number): Promise<Resource>;
  action(id: string, action: 'restart' | 'redeploy' | 'reconcile' | 'start' | 'stop' | 'purge' | 'refresh'): Promise<Resource>;
  getEvents(id: string): Promise<Event[]>;
  /** Container output, or with `build` a source app's latest build log. */
  getLogs(id: string, tail?: number, build?: boolean): Promise<LogLine[]>;
  revealSecret(id: string): Promise<string>;

  /** Starts a run of a job or backup now (409 RUN_IN_PROGRESS under overlap forbid). */
  runNow(id: string): Promise<Run>;
  /**
   * Restores a backup run (BAK-06, BAK-07). Returns the resource being restored into: a new database or
   * cache for mode "new", the source (phase updating) for "replace".
   */
  restore(id: string, req: RestoreRequest): Promise<Resource>;
  /** A job's or backup's runs, newest first (one page). */
  listRuns(id: string): Promise<Run[]>;
  /** One run with its output tail. */
  getRun(id: string, runId: string): Promise<RunDetail>;

  // v0.6 data endpoints (L3-002 §9.3).
  /** Sends to a queue or publishes to a topic. */
  sendMessage(id: string, message: SendMessage): Promise<SentMessage>;
  /** A queue's live stats and oldest messages, without receiving them. */
  listMessages(id: string, limit?: number): Promise<MessageList>;
  receiveMessages(id: string, max?: number, waitSeconds?: number): Promise<ReceivedMessage[]>;
  deleteMessages(id: string, receipts: string[]): Promise<DeletedMessages>;
  /** A topic's deliveries per subscription. */
  getDeliveries(id: string): Promise<DeliveryStats>;
  sendEmail(id: string, email: SendEmail): Promise<{ id: string }>;
  listEmails(id: string, limit?: number): Promise<EmailList>;
  getEmail(id: string, emailId: string): Promise<Email>;
  /** Converts one file of a transcoder's input folder now. */
  transcode(id: string, key: string): Promise<Run>;

  // v0.7 monitoring (L3-002 §10.4, §10.5).
  /** A resource's metrics over [from, to] (unix seconds); empty metrics means its standard set. */
  getMetrics(id: string, metrics: string[], from: number, to: number): Promise<MetricData>;
  getMachineMetrics(metrics: string[], from: number, to: number): Promise<MetricData>;
  /** Sends a test through a channel or an alert's channels, or checks a secret store. */
  test(id: string): Promise<TestResult>;
  listIncidents(id: string): Promise<Incident[]>;
  listNotifications(id: string): Promise<NotificationRecord[]>;
}

export type ApiMode = 'mock' | 'http';

export function resolveApiMode(env: { VITE_ENCLAVE_API?: string; PROD: boolean }): ApiMode {
  if (env.VITE_ENCLAVE_API === 'mock' || env.VITE_ENCLAVE_API === 'http') return env.VITE_ENCLAVE_API;
  return env.PROD ? 'http' : 'mock';
}

export function createApi(mode: ApiMode, mockOptions: MockOptions = {}): EnclaveApi {
  return mode === 'http' ? createHttpApi('/api/v1') : createMockApi(mockOptions);
}
