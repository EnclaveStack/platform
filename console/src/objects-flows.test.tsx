import { screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import { validateObjectKey, validateObjectTags } from './lib/validation';
import { renderConsole, signIn } from './test-console';

// A bucket's files in the console (L2-013 CON-74) through real routes on the mock adapter.

beforeEach(() => localStorage.clear());

describe('object rules', () => {
  it('match pkg/api', () => {
    expect(validateObjectKey('incoming/clip.mov')).toBeUndefined();
    for (const k of ['', '/a', 'a/', 'a//b', 'a/../b', './a']) expect(validateObjectKey(k)).toBeDefined();
    expect(validateObjectTags({ 'video-id': 'v42' })).toEqual({});
    expect(validateObjectTags({ Video_ID: 'v42' })).toHaveProperty(['tags.Video_ID']);
    expect(validateObjectTags({ a: '' })).toHaveProperty(['tags.a']);
  });
});

describe('bucket files in the console', () => {
  it('uploads into a new folder with a tag, browses, and deletes (CON-74)', async () => {
    const { user, api } = renderConsole('/buckets');
    await signIn(user);
    const bucket = await api.createResource('bucket', 'media', {});
    await waitFor(async () => expect((await api.getResource(bucket.id)).status.phase).toBe('ready'), { timeout: 3000 });
    await user.click(await screen.findByRole('link', { name: 'media' }));
    await user.click(await screen.findByRole('tab', { name: 'Objects' }));
    expect(await screen.findByText('No files yet')).toBeInTheDocument();

    // New folder, then upload into it with a tag.
    await user.click(screen.getByRole('button', { name: 'New folder' }));
    let dialog = await screen.findByRole('alertdialog', { name: 'New folder' });
    await user.type(within(dialog).getByLabelText('Name'), 'incoming');
    await user.click(within(dialog).getByRole('button', { name: 'Open folder' }));
    expect(await screen.findByText('This folder is empty')).toBeInTheDocument();
    await user.click(screen.getAllByRole('button', { name: 'Upload' })[0]!);
    dialog = await screen.findByRole('alertdialog', { name: 'Upload to media' });
    expect(within(dialog).getByLabelText('Folder')).toHaveValue('incoming/');
    await user.upload(within(dialog).getByLabelText('Choose files'), [
      new File(['video bytes'], 'clip.mov', { type: 'video/quicktime' }), new File(['hi'], 'notes.txt', { type: 'text/plain' })]);
    expect(within(within(dialog).getByRole('list', { name: 'Chosen files' })).getByText('clip.mov')).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Add tag' }));
    await user.type(within(dialog).getByLabelText('Tag 1 name'), 'Video_ID');
    await user.type(within(dialog).getByLabelText('Tag 1 value'), 'v42');
    await user.click(within(dialog).getByRole('button', { name: 'Upload' }));
    expect(await within(dialog).findAllByText(/lowercase letters, digits and hyphens/)).not.toHaveLength(0);
    await user.clear(within(dialog).getByLabelText('Tag 1 name'));
    await user.type(within(dialog).getByLabelText('Tag 1 name'), 'video-id');
    await user.click(within(dialog).getByRole('button', { name: 'Upload' }));
    const files = await screen.findByRole('table', { name: 'Files' });
    expect(within(files).getByText('clip.mov')).toBeInTheDocument();
    expect(within(files).getByText('11 bytes')).toBeInTheDocument();
    expect(within(files).getByRole('link', { name: 'Download incoming/clip.mov' })).toHaveAttribute('href', `/api/v1/resources/${bucket.id}/objects/incoming/clip.mov`);
    const list = await api.listObjects(bucket.id, 'incoming/');
    expect(list.items.map((o) => o.key)).toEqual(['incoming/clip.mov', 'incoming/notes.txt']);

    // Back to the top: the folder is there and opens.
    await user.click(screen.getByRole('button', { name: 'media' }));
    await user.click(await within(await screen.findByRole('table', { name: 'Files' })).findByRole('button', { name: 'incoming/' }));
    expect(await screen.findByText('notes.txt')).toBeInTheDocument();

    // Delete one.
    await user.click(screen.getByRole('button', { name: 'Delete incoming/notes.txt' }));
    dialog = await screen.findByRole('alertdialog', { name: 'Delete this file?' });
    await user.click(within(dialog).getByRole('button', { name: 'Delete file' }));
    await waitFor(() => expect(screen.queryByText('notes.txt')).not.toBeInTheDocument());
    const events = await api.getEvents(bucket.id);
    expect(events.map((e) => e.type)).toEqual(expect.arrayContaining(['object_uploaded', 'object_deleted']));
  });

  it('tells a viewer that only editors open files, and the API agrees', async () => {
    const { user, api } = renderConsole('/buckets', { seed: 'demo', as: { machineRole: null, projectRoles: { default: 'viewer' } } });
    await signIn(user);
    const bucket = (await api.listResources('bucket')).find((b) => b.project === 'default')!;
    await user.click(await screen.findByRole('link', { name: bucket.name }));
    await user.click(await screen.findByRole('tab', { name: 'Objects' }));
    expect(await screen.findByText("Only editors can open a bucket's files.")).toBeInTheDocument();
    await expect(api.listObjects(bucket.id, '')).rejects.toMatchObject({ code: 'FORBIDDEN' });
  });
});
