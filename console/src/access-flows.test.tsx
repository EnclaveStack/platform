import { screen, waitFor, within } from '@testing-library/react';
import { beforeEach, describe, expect, it } from 'vitest';
import type { AppSpec, JobSpec, Resource } from './api/types';
import { cliCommand, tofuBlock } from './lib/codegen';
import { validateApp, validateBucketAccess, validateJob } from './lib/validation';
import { renderConsole, signIn } from './test-console';

// Per-app bucket access in the console (L2-011 CON-70, CON-71) through real routes on the mock adapter,
// and the rules and code the console shares with the server and the other surfaces.

beforeEach(() => localStorage.clear());

describe('bucket access rules', () => {
  it('match pkg/api: levels, duplicates, the limit and the five variables', () => {
    expect(validateBucketAccess([{ bucket: 'media', access: 'read' }, { bucket: 'invoices', access: 'readwrite' }])).toEqual({});
    expect(validateBucketAccess([{ bucket: 'media', access: 'write' as never }])).toHaveProperty(['spec.bucketAccess[0].access']);
    expect(validateBucketAccess([{ bucket: 'media', access: 'read' }, { bucket: 'media', access: 'readwrite' }])).toHaveProperty(['spec.bucketAccess[1].bucket']);
    expect(validateBucketAccess(Array.from({ length: 11 }, (_, i) => ({ bucket: `bucket-${i}`, access: 'read' as const })))).toHaveProperty(['spec.bucketAccess']);
    expect(validateApp({ image: 'nginx', env: { AWS_ACCESS_KEY_ID: 'x' }, bucketAccess: [{ bucket: 'media', access: 'read' }] })).toHaveProperty(['spec.env.AWS_ACCESS_KEY_ID']);
    expect(validateApp({ image: 'nginx', env: { AWS_REGION: 'eu-west-1' } })).toEqual({}); // without access they are ordinary
    expect(validateJob({ schedule: '@daily', app: 'web', bucketAccess: [{ bucket: 'media', access: 'read' }] } as JobSpec)).toHaveProperty(['spec.bucketAccess']);
  });

  it('show up in the CLI command and the OpenTofu block', () => {
    const app = { kind: 'app', name: 'web', project: 'default', spec: { image: 'nginx:1.27', port: 80, bucketAccess: [{ bucket: 'media', access: 'readwrite' }] } } as unknown as Resource;
    expect(cliCommand(app)).toContain("--bucket media:readwrite");
    expect(tofuBlock(app)).toContain('bucket_access = [\n    { bucket = "media", access = "readwrite" },\n  ]');
  });
});

describe('bucket access in the console', () => {
  it('gives an app read on a bucket, shows it, widens it and takes it away (CON-70, CON-71)', async () => {
    const { user, api } = renderConsole('/buckets');
    await signIn(user);
    await api.createResource('bucket', 'media', {});
    await api.createResource('bucket', 'invoices', {});

    await user.click(await screen.findByRole('link', { name: /Apps/ }));
    await user.click((await screen.findAllByRole('button', { name: 'New app' }))[0]!);
    let panel = await screen.findByRole('dialog', { name: 'New app' });
    await user.type(within(panel).getByLabelText('Name'), 'storefront');
    await user.type(within(panel).getByLabelText('Image'), 'nginx:1.27');
    expect(within(panel).getByText(/AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_REGION, AWS_ENDPOINT_URL_S3, AWS_ENDPOINT_URL/)).toBeInTheDocument();
    await user.click(await within(panel).findByRole('button', { name: 'Add bucket' }));
    await user.selectOptions(within(panel).getByLabelText('Bucket 1'), 'media');
    expect(within(panel).getByLabelText('Bucket 1 access')).toHaveValue('read');
    // The same bucket twice is caught before it is sent.
    await user.click(within(panel).getByRole('button', { name: 'Add bucket' }));
    await user.selectOptions(within(panel).getByLabelText('Bucket 2'), 'media');
    await user.click(within(panel).getByRole('button', { name: 'Create app' }));
    expect(await within(panel).findByText('This bucket is already in the list.')).toBeInTheDocument();
    await user.click(within(panel).getByRole('button', { name: 'Remove bucket 2' }));
    await user.click(within(panel).getByRole('button', { name: 'Create app' }));
    await waitFor(async () => expect(((await api.listResources('app'))[0]?.spec as AppSpec | undefined)?.bucketAccess).toEqual([{ bucket: 'media', access: 'read' }]));

    // Its page: the bucket and level, the keys' secret, the variables.
    const card = await screen.findByRole('region', { name: 'Bucket access' });
    await waitFor(() => expect(within(card).getByRole('link', { name: 'media' })).toBeInTheDocument());
    expect(within(card).getByText('Read')).toBeInTheDocument();
    await waitFor(() => expect(within(card).getByRole('link', { name: 'storefront-bucket-credentials' })).toBeInTheDocument(), { timeout: 3000 });
    expect(within(card).getByText(/AWS_ACCESS_KEY_ID/)).toBeInTheDocument();
    expect(screen.queryByText('bucketCredentialsSecret')).not.toBeInTheDocument(); // labelled in Details, not the raw key

    // A bucket an app can access can't be deleted.
    const media = (await api.listResources('bucket')).find((b) => b.name === 'media')!;
    await expect(api.deleteResource(media.id)).rejects.toMatchObject({ code: 'RESOURCE_IN_USE' });

    // Edit: read and write, then no access at all.
    await user.click(screen.getByRole('button', { name: 'Edit' }));
    panel = await screen.findByRole('dialog', { name: 'Edit app storefront' });
    await user.selectOptions(within(panel).getByLabelText('Bucket 1 access'), 'readwrite');
    await user.click(within(panel).getByRole('button', { name: /Save/ }));
    await waitFor(async () => expect(((await api.listResources('app'))[0]?.spec as AppSpec).bucketAccess).toEqual([{ bucket: 'media', access: 'readwrite' }]));
    await user.click(screen.getByRole('button', { name: 'Edit' }));
    panel = await screen.findByRole('dialog', { name: 'Edit app storefront' });
    await user.click(within(panel).getByRole('button', { name: 'Remove bucket 1' }));
    await user.click(within(panel).getByRole('button', { name: /Save/ }));
    await waitFor(async () => expect(((await api.listResources('app'))[0]?.spec as AppSpec).bucketAccess).toBeUndefined());
    await waitFor(async () => expect((await api.listResources('secret')).some((s) => s.name === 'storefront-bucket-credentials')).toBe(false));
    await expect(api.deleteResource(media.id)).resolves.toBeTruthy();
  });

  it('a job that runs as an app says it uses the app’s access; one from an image has its own', async () => {
    const { user, api } = renderConsole('/jobs');
    await signIn(user);
    await api.createResource('bucket', 'reports', {});
    await api.createResource('app', 'api', { image: 'nginx:1.27', port: 80, bucketAccess: [{ bucket: 'reports', access: 'read' }] });
    await user.click((await screen.findAllByRole('button', { name: 'New job' }))[0]!);
    const panel = await screen.findByRole('dialog', { name: 'New job' });
    // Run with an app (the default): the app's access, nothing to set.
    expect(await within(panel).findByText("A job that runs as an app uses the app's bucket access and keys.")).toBeInTheDocument();
    expect(within(panel).queryByRole('button', { name: 'Add bucket' })).not.toBeInTheDocument();
    // From an image: access of its own.
    await user.selectOptions(within(panel).getByLabelText('Run with'), 'image');
    await user.type(within(panel).getByLabelText('Name'), 'export');
    await user.type(within(panel).getByLabelText('Image'), 'busybox:1.36');
    await user.type(within(panel).getByLabelText('Schedule'), '@daily');
    await user.click(await within(panel).findByRole('button', { name: 'Add bucket' }));
    await user.selectOptions(within(panel).getByLabelText('Bucket 1'), 'reports');
    await user.selectOptions(within(panel).getByLabelText('Bucket 1 access'), 'readwrite');
    await user.click(within(panel).getByRole('button', { name: 'Create job' }));
    await waitFor(async () => expect(((await api.listResources('job')).find((j) => j.name === 'export')?.spec as JobSpec | undefined)?.bucketAccess)
      .toEqual([{ bucket: 'reports', access: 'readwrite' }]));
  });
});
