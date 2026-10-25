import { screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import type { Resource, TranscoderSpec } from './api/types';
import { cliCommand, tofuBlock } from './lib/codegen';
import { runFolder, transcodeFiles } from './lib/transcode';
import { validateTranscoder } from './lib/validation';
import { renderConsole, signIn } from './test-console';

// Transcoder renditions, the HLS ladder and notify in the console (L2-012 CON-72, CON-73) through real
// routes on the mock adapter, and the rules and code the console shares with the server and other surfaces.

beforeEach(() => localStorage.clear());

const base = { input: { bucket: 'media', prefix: 'incoming/' }, output: { bucket: 'media', prefix: 'processed/{meta:video-id}/' } };
const rung = { name: '720p', maxWidth: 720, maxHeight: 1280, videoKbps: 2000 };

describe('transcoder output rules', () => {
  it('match pkg/api', () => {
    expect(validateTranscoder({ ...base, hls: { rungs: [rung] }, renditions: [{ name: 'selfie', container: 'mp4', maxWidth: 720, maxHeight: 1280, videoKbps: 2500 }],
      notify: { webhook: { url: 'https://video.example.com/done' }, topic: 'events' } })).toEqual({});
    expect(validateTranscoder(base)).toHaveProperty(['spec.presets']);
    expect(validateTranscoder({ ...base, hls: { segmentSeconds: 5, rungs: [rung] } })).toHaveProperty(['spec.hls.segmentSeconds']);
    expect(validateTranscoder({ ...base, hls: { rungs: [rung, rung] } })).toHaveProperty(['spec.hls.rungs[1].name']);
    expect(validateTranscoder({ ...base, renditions: [{ name: 'a', container: 'mp4', maxWidth: 721, maxHeight: 1280, videoKbps: 2500 }] })).toHaveProperty(['spec.renditions[0].maxWidth']);
    expect(validateTranscoder({ ...base, presets: ['mp4-720p'], renditions: [{ name: 'mp4-720p', container: 'mp4', maxWidth: 720, maxHeight: 1280, videoKbps: 900 }] }))
      .toHaveProperty(['spec.renditions[0].name']);
    expect(validateTranscoder({ ...base, presets: ['mp3'], notify: {} })).toHaveProperty(['spec.notify']);
    expect(validateTranscoder({ ...base, presets: ['mp3'], notify: { webhook: { url: 'http://10.0.0.5/x' } } })).toHaveProperty(['spec.notify.webhook.url']);
    expect(validateTranscoder({ ...base, presets: ['mp3'], output: { bucket: 'media', prefix: 'out/{date}/' } })).toHaveProperty(['spec.output.prefix']);
    expect(validateTranscoder({ ...base, presets: ['mp3'], output: { bucket: 'media', prefix: '{meta:id}/' } })).toHaveProperty(['spec.output.prefix']);
  });

  it('name files and folders as the server does', () => {
    expect(transcodeFiles({ renditions: [{ name: 'selfie', container: 'mp4', maxWidth: 720, maxHeight: 1280, videoKbps: 2500 }], hls: { rungs: [rung] } }))
      .toEqual(['selfie.mp4', 'hls/720p/index.m3u8', 'hls/720p/segment-000.ts', 'hls/master.m3u8']);
    expect(runFolder('processed/{meta:video-id}/', 'incoming/', 'incoming/clip.mov', { 'video-id': 'v42' })).toEqual({ folder: 'processed/v42/' });
    expect(runFolder('media/', 'videos/', 'videos/2026/cat.mov')).toEqual({ folder: 'media/2026/cat/' });
    expect(runFolder('p/{meta:id}/', '', 'a.mov', { id: '..' })).toHaveProperty('error');
  });

  it('show up in the CLI command and the OpenTofu block', () => {
    const tc = { kind: 'transcoder', name: 'videos', project: 'default', spec: { ...base, hls: { name: 'hls', segmentSeconds: 6, gopSeconds: 2, audioKbps: 128, rungs: [rung] },
      renditions: [{ name: 'web', container: 'webm', maxWidth: 1280, maxHeight: 720, videoKbps: 1500, audioKbps: 96, gopSeconds: 2 }],
      notify: { webhook: { url: 'https://video.example.com/done', secret: 'hook-key' }, topic: 'events' } } } as unknown as Resource;
    const cli = cliCommand(tc);
    for (const want of ["--output 'media/processed/{meta:video-id}/'", '--rendition web:webm:1280x720:1500:96', '--hls-rung 720p:720x1280:2000',
      '--notify-webhook https://video.example.com/done', '--notify-secret hook-key', '--notify-topic events']) expect(cli).toContain(want);
    expect(cli).not.toContain('--hls-segment');
    const hcl = tofuBlock(tc);
    expect(hcl).toContain('{ name = "web", max_width = 1280, max_height = 720, video_kbps = 1500, container = "webm", audio_kbps = 96 },');
    expect(hcl).toContain('      { name = "720p", max_width = 720, max_height = 1280, video_kbps = 2000 },');
    expect(hcl).toContain('    webhook_secret = "hook-key"');
    expect(hcl).not.toContain('presets');
  });
});

describe('transcoder outputs in the console', () => {
  it('makes a ladder, a rendition and a webhook, shows them, and converts a file into them (CON-72, CON-73)', async () => {
    const { user, api } = renderConsole('/transcoders');
    await signIn(user);
    await api.createResource('bucket', 'media', {});
    await api.createResource('secret', 'hook-key', { value: 's3cret' });
    await api.createResource('topic', 'video-events', {});
    await user.click((await screen.findAllByRole('button', { name: 'New transcoder' }))[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New transcoder' });
    await user.type(within(panel).getByLabelText('Name'), 'videos');
    const [inBucket, outBucket] = within(panel).getAllByLabelText('Bucket');
    await user.selectOptions(inBucket!, 'media');
    await user.type(within(panel).getAllByLabelText('Folder')[0]!, 'incoming/');
    await user.selectOptions(outBucket!, 'media');
    // Only the ladder and a rendition: no presets.
    for (const p of ['mp4-720p', 'thumbnail']) await user.click(within(panel).getByRole('checkbox', { name: new RegExp(`^${p}`) }));
    await user.click(within(panel).getByRole('checkbox', { name: /adaptive HLS stream/ }));
    expect(within(panel).getByLabelText('Rung 3 name')).toHaveValue('1080p'); // the starter ladder
    await user.click(within(panel).getByRole('button', { name: 'Remove rung 3' }));
    await user.click(within(panel).getByRole('button', { name: 'Add rendition' }));
    await user.type(within(panel).getByLabelText('Rendition 1 name'), 'selfie');
    await user.clear(within(panel).getByLabelText('Rendition 1 width'));
    await user.type(within(panel).getByLabelText('Rendition 1 width'), '721'); // odd: caught before it is sent
    await user.type(within(panel).getByLabelText('Webhook URL'), 'https://video.example.com/done');
    await user.selectOptions(within(panel).getByLabelText('Signing secret'), 'hook-key');
    await user.selectOptions(within(panel).getByLabelText('Topic'), 'video-events');
    await user.click(within(panel).getByRole('button', { name: 'Create transcoder' }));
    expect(await within(panel).findByText(/Use an even number/)).toBeInTheDocument();
    await user.clear(within(panel).getByLabelText('Rendition 1 width'));
    await user.type(within(panel).getByLabelText('Rendition 1 width'), '720');
    await user.clear(within(panel).getByLabelText('Rendition 1 height'));
    await user.type(within(panel).getByLabelText('Rendition 1 height'), '1280');
    await user.click(within(panel).getByRole('button', { name: 'Create transcoder' }));
    await waitFor(() => expect(screen.getByText('Ready')).toBeInTheDocument(), { timeout: 3000 });
    const spec = (await api.listResources('transcoder'))[0]!.spec as TranscoderSpec;
    expect(spec.presets).toBeUndefined();
    expect(spec.hls?.rungs.map((r) => r.name)).toEqual(['360p', '720p']);
    expect(spec.renditions?.[0]).toMatchObject({ name: 'selfie', container: 'mp4', maxWidth: 720, maxHeight: 1280, audioKbps: 128 });
    expect(spec.notify).toEqual({ webhook: { url: 'https://video.example.com/done', secret: 'hook-key' }, topic: 'video-events' });

    // The page: a card each.
    const ladder = await screen.findByRole('region', { name: 'HLS ladder' });
    expect(within(ladder).getByText('hls/master.m3u8')).toBeInTheDocument();
    expect(within(ladder).getByText('360p')).toBeInTheDocument();
    expect(within(await screen.findByRole('region', { name: 'Renditions' })).getByText('selfie.mp4')).toBeInTheDocument();
    expect(within(screen.getByRole('region', { name: 'Notify' })).getByText('signed with hook-key')).toBeInTheDocument();

    // The secret and topic it names can't be deleted.
    const sec = (await api.listResources('secret')).find((x) => x.name === 'hook-key')!;
    await expect(api.deleteResource(sec.id)).rejects.toMatchObject({ code: 'RESOURCE_IN_USE' });

    // Edit: drop notify and the rendition; the ladder stays.
    await user.click(screen.getByRole('button', { name: 'Edit' }));
    const edit = await screen.findByRole('dialog', { name: 'Edit transcoder videos' });
    await user.clear(within(edit).getByLabelText('Webhook URL'));
    await user.selectOptions(within(edit).getByLabelText('Topic'), '');
    await user.click(within(edit).getByRole('button', { name: 'Remove rendition 1' }));
    await user.click(within(edit).getByRole('button', { name: /Save/ }));
    await waitFor(async () => {
      const s = (await api.listResources('transcoder'))[0]!.spec as TranscoderSpec;
      expect(s.notify).toBeUndefined();
      expect(s.renditions).toBeUndefined();
      expect(s.hls?.rungs).toHaveLength(2);
    });
    await expect(api.deleteResource(sec.id)).resolves.toBeTruthy();
  });

  it('a run lists the files it wrote with the master playlist first; a folder needing a tag fails without it', async () => {
    const { user, api } = renderConsole('/transcoders');
    await signIn(user);
    await api.createResource('bucket', 'media', {});
    const plain = await api.createResource('transcoder', 'plain', { input: { bucket: 'media', prefix: 'in/' }, output: { bucket: 'media', prefix: 'out/' }, hls: { rungs: [rung] } });
    const tagged = await api.createResource('transcoder', 'tagged', { input: { bucket: 'media', prefix: 'up/' }, output: { bucket: 'media', prefix: 'done/{meta:video-id}/' }, presets: ['mp3'] });
    // Both must be ready before a run: the second one is still provisioning on a slow machine.
    for (const t of [plain, tagged]) await waitFor(async () => expect((await api.getResource(t.id)).status.phase).toBe('ready'), { timeout: 3000 });
    await api.transcode(plain.id, 'in/clip.mov');
    await api.transcode(tagged.id, 'up/clip.mov');
    await waitFor(async () => expect((await api.listRuns(tagged.id))[0]?.status).toBe('failed'), { timeout: 3000 });
    expect((await api.getRun(tagged.id, (await api.listRuns(tagged.id))[0]!.id)).message).toContain('has no tag video-id');
    await user.click(await screen.findByRole('link', { name: 'plain' }));
    await user.click(await screen.findByRole('tab', { name: 'Runs' }));
    await user.click(await screen.findByRole('button', { name: /^Output of run/ }, { timeout: 3000 }));
    const files = await screen.findByLabelText('Files written');
    const keys = within(files).getAllByText(/^out\/clip\//).map((e) => e.textContent);
    expect(keys[0]).toBe('out/clip/hls/master.m3u8');
    expect(keys).toContain('out/clip/hls/720p/segment-000.ts');
    expect(within(files).getByText('Master playlist')).toBeInTheDocument();
  });
});
