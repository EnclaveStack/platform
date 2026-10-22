import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { createMemoryRouter, RouterProvider } from 'react-router';
import { describe, expect, it } from 'vitest';
import { routes } from './App';
import { ApiProvider } from './api/context';
import { createMockApi, DEMO_ADMIN } from './api/mock';
import type { CDNSpec, DatabaseSpec, QueueSpec, TopicSpec, TranscoderSpec } from './api/types';
import { SessionProvider } from './auth/session';

// v0.6 operator flows (L2-001 CON-37 – CON-44) through real routes and components on the mock adapter.

function renderConsole(path: string) {
  const api = createMockApi({ latencyMs: 0, stepMs: 30, storage: null });
  const router = createMemoryRouter(routes, { initialEntries: [path] });
  render(
    <ApiProvider api={api}>
      <SessionProvider>
        <RouterProvider router={router} />
      </SessionProvider>
    </ApiProvider>,
  );
  return { router, api, user: userEvent.setup() };
}

async function signIn(user: ReturnType<typeof userEvent.setup>) {
  await user.type(await screen.findByLabelText('Email'), DEMO_ADMIN.email);
  await user.type(screen.getByLabelText('Password'), DEMO_ADMIN.password);
  await user.click(screen.getByRole('button', { name: 'Sign in' }));
}

async function openNew(user: ReturnType<typeof userEvent.setup>, noun: string) {
  await user.click((await screen.findAllByRole('button', { name: `New ${noun}` }))[0]!);
  return screen.findByRole('dialog', { name: `New ${noun}` });
}

describe('data and messaging in the console', () => {
  it('creates a MySQL database and shows its connection string (CON-37)', async () => {
    const { user, api } = renderConsole('/databases');
    await signIn(user);
    const panel = await openNew(user, 'database');
    await user.type(within(panel).getByLabelText('Name'), 'legacy');
    await user.click(within(panel).getByRole('radio', { name: /MySQL/ }));
    expect(within(panel).getByLabelText('MySQL version')).toHaveValue('8.4');
    await user.click(within(panel).getByRole('button', { name: 'Create database' }));
    expect(await screen.findByRole('heading', { name: 'legacy' })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    expect(screen.getByText('MySQL 8.4')).toBeInTheDocument();
    expect(screen.getByText(/^mysql:\/\/legacy:<password>@127\.0\.0\.1:\d+\/legacy$/)).toBeInTheDocument();
    expect(((await api.listResources('database'))[0]!.spec as DatabaseSpec).engine).toBe('mysql');
  });

  it('creates a queue with a dead-letter queue, then sends, receives and deletes a message (CON-38)', async () => {
    const { user, api } = renderConsole('/queues');
    await signIn(user);
    await api.createResource('queue', 'failed', {});
    const panel = await openNew(user, 'queue');
    await user.type(within(panel).getByLabelText('Name'), 'emails');
    await user.clear(within(panel).getByLabelText('Maximum receives'));
    await user.type(within(panel).getByLabelText('Maximum receives'), '5');
    await user.selectOptions(await within(panel).findByLabelText('Dead-letter queue'), 'failed');
    await user.click(within(panel).getByRole('button', { name: 'Create queue' }));
    expect(await screen.findByRole('heading', { name: 'emails' })).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    const q = (await api.listResources('queue')).find((x) => x.name === 'emails')!;
    expect(q.spec as QueueSpec).toMatchObject({ maxReceives: 5, deadLetterQueue: 'failed' });
    expect(screen.getByText('nats://es-system-broker:4222 (user q-emails, subject es.q.emails)')).toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: 'Messages' }));
    await user.type(await screen.findByLabelText('Message'), 'hello');
    await user.click(screen.getByRole('button', { name: 'Send' }));
    expect(await screen.findByText('1 waiting')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Receive up to 10' }));
    const received = await screen.findByRole('table', { name: 'Received messages' });
    await user.click(within(received).getByRole('button', { name: 'Delete' }));
    await waitFor(() => expect(screen.queryByRole('table', { name: 'Received messages' })).not.toBeInTheDocument());
    await waitFor(() => expect(screen.getByText('0 in flight')).toBeInTheDocument(), { timeout: 3000 });
  });

  it('subscribes a queue and a webhook to a topic and publishes (CON-39)', async () => {
    const { user, api } = renderConsole('/topics');
    await signIn(user);
    await api.createResource('queue', 'inbox', {});
    const panel = await openNew(user, 'topic');
    await user.type(within(panel).getByLabelText('Name'), 'orders');
    await user.click(within(panel).getByRole('button', { name: 'Add subscription' }));
    await user.selectOptions(within(panel).getByLabelText('Queue'), 'inbox');
    await user.click(within(panel).getByRole('button', { name: 'Add subscription' }));
    await user.selectOptions(within(panel).getAllByLabelText('Deliver to')[1]!, 'url');
    await user.type(within(panel).getByLabelText('URL'), 'http://example.com/hook');
    await user.click(within(panel).getByRole('button', { name: 'Create topic' }));
    expect(within(panel).getByText(/https:\/\/ URL \(http:\/\/ only for localhost\)/)).toBeInTheDocument();
    await user.clear(within(panel).getByLabelText('URL'));
    await user.type(within(panel).getByLabelText('URL'), 'https://hooks.example.com/orders');
    await user.click(within(panel).getByRole('button', { name: 'Create topic' }));
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    const t = (await api.listResources('topic'))[0]!;
    expect((t.spec as TopicSpec).subscriptions).toEqual([{ queue: 'inbox' }, { url: 'https://hooks.example.com/orders' }]);

    await user.click(screen.getByRole('tab', { name: 'Deliveries' }));
    await user.type(await screen.findByLabelText('Message'), '{{"id":1}');
    await user.click(screen.getByRole('button', { name: 'Publish' }));
    expect(await screen.findByText('1 published so far')).toBeInTheDocument();
    expect(screen.getByText('queue/inbox')).toBeInTheDocument();
  });

  it('sends a test email and opens it (CON-43)', async () => {
    const { user, api } = renderConsole('/email');
    await signIn(user);
    const e = await api.createResource('email', 'shop-mail', { domain: 'example.com' });
    await user.click(await screen.findByRole('link', { name: /shop-mail/ }));
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    await user.click(screen.getByRole('tab', { name: 'Messages' }));
    await user.click(await screen.findByRole('button', { name: 'Send a test message' }));
    const drawer = await screen.findByRole('dialog', { name: 'Send a test message' });
    expect(within(drawer).getByLabelText('From')).toHaveValue('test@example.com');
    await user.type(within(drawer).getByLabelText('To'), 'a@b.test');
    await user.click(within(drawer).getByRole('button', { name: 'Send' }));
    await user.click(await screen.findByRole('button', { name: 'A test from EnclaveStack' }));
    const viewer = await screen.findByRole('dialog', { name: 'A test from EnclaveStack' });
    expect(within(viewer).getByText('Hello from EnclaveStack.')).toBeInTheDocument();
    expect(within(viewer).getByText(/Captured, not delivered/)).toBeInTheDocument();
    expect((await api.listEmails(e.id)).total).toBe(1);
  });

  it('creates a transcoder and a bucket CDN, and converts a file by hand (CON-41, CON-42)', async () => {
    const { user, api } = renderConsole('/transcoders');
    await signIn(user);
    await api.createResource('bucket', 'uploads', {});
    const panel = await openNew(user, 'transcoder');
    await user.type(within(panel).getByLabelText('Name'), 'videos');
    const [inBucket, outBucket] = within(panel).getAllByLabelText('Bucket');
    await user.selectOptions(inBucket!, 'uploads');
    await user.type(within(panel).getAllByLabelText('Folder')[0]!, 'videos');
    await user.selectOptions(outBucket!, 'uploads');
    expect(within(panel).getByText(/Files in uploads\/videos\/ → uploads\/transcoded\//)).toBeInTheDocument();
    await user.click(within(panel).getByRole('checkbox', { name: /^mp3/ }));
    await user.click(within(panel).getByRole('button', { name: 'Create transcoder' }));
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    const tc = (await api.listResources('transcoder'))[0]!;
    expect((tc.spec as TranscoderSpec).presets).toEqual(['mp4-720p', 'thumbnail', 'mp3']);
    await user.click(screen.getByRole('button', { name: 'Transcode a file' }));
    const dialog = await screen.findByRole('alertdialog', { name: 'Transcode a file' });
    await user.type(within(dialog).getByLabelText('Object key'), 'cat.mov');
    await user.click(within(dialog).getByRole('button', { name: 'Transcode' }));
    await waitFor(() => expect(screen.getAllByText('Succeeded').length).toBeGreaterThan(0), { timeout: 3000 });

    await user.click(screen.getByRole('link', { name: /^CDN/ }));
    const cdnPanel = await openNew(user, 'CDN');
    await user.type(within(cdnPanel).getByLabelText('Name'), 'files');
    await user.type(within(cdnPanel).getByLabelText('Hostname'), 'files.localhost');
    await user.click(within(cdnPanel).getByRole('radio', { name: /A bucket/ }));
    await user.selectOptions(within(cdnPanel).getByLabelText('Bucket'), 'uploads');
    await user.click(within(cdnPanel).getByRole('button', { name: 'Create CDN' }));
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    expect(((await api.listResources('cdn'))[0]!.spec as CDNSpec).origin).toBe('bucket/uploads');
    expect(screen.getByText('https://files.localhost')).toBeInTheDocument();
  });
});
