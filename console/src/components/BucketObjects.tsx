import { Alert, Button, buttonClass, Card, ConfirmDialog, EmptyState, Field, FieldError, FilePicker, FormRow, Icons, IconButton, Inline, Input, Stack, Table, Text, useToast, VisuallyHidden } from '@enclavestack/ui';
import { useState } from 'react';
import { useApi } from '../api/context';
import { ApiError, type ObjectInfo, type Resource } from '../api/types';
import { formatBytes } from '../lib/format';
import { useLoad } from '../lib/useLoad';
import { validateObjectKey, validateObjectTags } from '../lib/validation';

// A bucket's files (L2-013 CON-74, L2.5-002 §19): folders one at a time, upload with tags, download, delete.

const errorText = (e: unknown) => (e instanceof ApiError ? e.message + (e.details.length ? ' ' + e.details.map((d) => d.message).join(' ') : '') : 'That did not work. Try again.');

/** The last part of a key or folder, as the table shows it. */
const leaf = (key: string, prefix: string) => key.slice(prefix.length);

export function BucketObjects({ bucket, canEdit }: { bucket: Resource; canEdit: boolean }) {
  const api = useApi();
  const toast = useToast();
  const [prefix, setPrefix] = useState('');
  const [pages, setPages] = useState(1);
  const list = useLoad(`objects:${bucket.id}:${prefix}:${pages}:${canEdit}`, async () => {
    if (!canEdit) return null; // a viewer is refused; don't ask
    const out = await api.listObjects(bucket.id, prefix);
    for (let i = 1; i < pages && out.nextCursor; i++) {
      const next = await api.listObjects(bucket.id, prefix, out.nextCursor);
      out.folders.push(...next.folders);
      out.items.push(...next.items);
      out.nextCursor = next.nextCursor;
    }
    return out;
  });
  const [uploading, setUploading] = useState(false);
  const [newFolder, setNewFolder] = useState(false);
  const [deleting, setDeleting] = useState<ObjectInfo | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (!canEdit) return <Card><Text tone="muted">Only editors can open a bucket's files.</Text></Card>;

  const open = (p: string) => {
    setPrefix(p);
    setPages(1);
  };
  const parts = prefix.split('/').filter(Boolean);
  const data = list.data;
  const empty = data && data.folders.length === 0 && data.items.length === 0;

  return (
    <Stack gap={4}>
      <Inline gap={2} wrap justify="between">
        <nav aria-label="Folder">
          <Inline gap={1} wrap>
            <Button size="sm" variant="ghost" onClick={() => open('')} aria-current={prefix === '' ? 'page' : undefined}>{bucket.name}</Button>
            {parts.map((p, i) => (
              <Inline key={i} gap={1}>
                <Text tone="subtle">/</Text>
                <Button size="sm" variant="ghost" onClick={() => open(parts.slice(0, i + 1).join('/') + '/')} aria-current={i === parts.length - 1 ? 'page' : undefined}>{p}</Button>
              </Inline>
            ))}
          </Inline>
        </nav>
        <Inline gap={2}>
          <Button size="sm" icon={<Icons.FolderPlus />} onClick={() => setNewFolder(true)}>New folder</Button>
          <Button size="sm" variant="dark" icon={<Icons.Upload />} onClick={() => setUploading(true)}>Upload</Button>
        </Inline>
      </Inline>
      {(error || list.error) && <Alert>{error ?? list.error?.message}</Alert>}
      {empty ? (
        <EmptyState icon={<Icons.Folder />} title={prefix ? 'This folder is empty' : 'No files yet'}
          action={<Button variant="dark" icon={<Icons.Upload />} onClick={() => setUploading(true)}>Upload</Button>}>
          {prefix ? 'Upload a file to create this folder.' : <>Upload one here, or with <Text mono>enclave bucket upload</Text> or any S3 client.</>}
        </EmptyState>
      ) : (
        <Card padding="flush">
          <Table aria-label="Files">
            <thead>
              <tr><th scope="col">Name</th><th scope="col">Size</th><th scope="col">Changed</th><th scope="col"><VisuallyHidden>Actions</VisuallyHidden></th></tr>
            </thead>
            <tbody>
              {data?.folders.map((f) => (
                <tr key={f}>
                  <td><Button size="sm" variant="ghost" icon={<Icons.Folder />} onClick={() => open(f)}>{leaf(f, prefix)}</Button></td>
                  <td><Text tone="subtle">Folder</Text></td>
                  <td />
                  <td />
                </tr>
              ))}
              {data?.items.map((o) => (
                <tr key={o.key}>
                  <td><Inline gap={2}><Icons.File aria-hidden="true" /><Text mono size="sm">{leaf(o.key, prefix)}</Text></Inline></td>
                  <td><Text tone="muted">{formatBytes(o.size)}</Text></td>
                  <td><Text tone="muted" size="sm">{new Date(o.modified).toLocaleString()}</Text></td>
                  <td>
                    <Inline gap={1} justify="end">
                      <a className={buttonClass({ size: 'sm', variant: 'ghost' })} href={api.objectUrl(bucket.id, o.key)} download={leaf(o.key, prefix)} aria-label={`Download ${o.key}`}>
                        <Icons.Download aria-hidden="true" />
                      </a>
                      <IconButton size="sm" variant="ghost" label={`Delete ${o.key}`} icon={<Icons.Trash2 />} onClick={() => setDeleting(o)} />
                    </Inline>
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
          {data?.nextCursor && <Inline justify="center"><Button size="sm" variant="ghost" onClick={() => setPages(pages + 1)}>Load more</Button></Inline>}
        </Card>
      )}
      {uploading && (
        <UploadDialog bucket={bucket} folder={prefix} onClose={() => setUploading(false)} onDone={(n, folder) => {
          setUploading(false);
          toast(`${n} file${n === 1 ? '' : 's'} uploaded`);
          if (folder !== prefix) open(folder);
          else list.reload();
        }} />
      )}
      {newFolder && <NewFolderDialog prefix={prefix} onClose={() => setNewFolder(false)} onOpen={(p) => { setNewFolder(false); open(p); }} />}
      {deleting && (
        <ConfirmDialog title="Delete this file?" danger busy={busy} confirmLabel="Delete file" error={error ?? undefined}
          body={<><Text mono>{deleting.key}</Text> is removed from {bucket.name}. Apps that link to it get “not found”.</>}
          onCancel={() => { setDeleting(null); setError(null); }}
          onConfirm={async () => {
            setBusy(true);
            try {
              await api.deleteObject(bucket.id, deleting.key);
              toast('File deleted');
              setDeleting(null);
              setError(null);
              list.reload();
            } catch (e) {
              setError(errorText(e));
            } finally {
              setBusy(false);
            }
          }} />
      )}
    </Stack>
  );
}

interface TagRow {
  name: string;
  value: string;
}

/** Files into a folder, with tags every file gets (OBJ-02). */
function UploadDialog({ bucket, folder: start, onClose, onDone }: { bucket: Resource; folder: string; onClose: () => void; onDone: (count: number, folder: string) => void }) {
  const api = useApi();
  const [files, setFiles] = useState<File[]>([]);
  const [folder, setFolder] = useState(start);
  const [tags, setTags] = useState<TagRow[]>([]);
  const [done, setDone] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const normal = folder.trim().replace(/^\/+/, '').replace(/([^/])$/, '$1/');
  const tagMap = Object.fromEntries(tags.filter((t) => t.name || t.value).map((t) => [t.name.trim(), t.value]));
  const tagErrors = validateObjectTags(tagMap);
  async function upload() {
    const keyError = files.map((f) => validateObjectKey(normal + f.name)).find(Boolean);
    if (!files.length) return setError('Choose at least one file.');
    if (keyError) return setError(keyError);
    if (Object.keys(tagErrors).length) return setError(Object.values(tagErrors)[0] ?? null);
    setBusy(true);
    setError(null);
    try {
      for (const [i, f] of files.entries()) {
        await api.uploadObject(bucket.id, normal + f.name, f, tagMap);
        setDone(i + 1);
      }
      onDone(files.length, normal);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <ConfirmDialog title={`Upload to ${bucket.name}`} confirmLabel={busy ? `Uploading ${done + 1} of ${files.length}…` : 'Upload'} busy={busy} error={error ?? undefined}
      onCancel={onClose} onConfirm={() => void upload()}
      body={
        <Stack gap={3}>
          <FilePicker id="f-upload-files" label="Choose files" multiple files={files} onChange={setFiles} sizeText={formatBytes} />
          <Field id="f-upload-folder" label="Folder" hint="Where the files go; empty for the top of the bucket. A new folder is made by uploading into it.">
            <Input id="f-upload-folder" mono value={folder} onChange={(e) => setFolder(e.target.value)} placeholder="incoming/" spellCheck={false} />
          </Field>
          <Stack gap={2}>
            <Text size="sm" tone="muted">Tags (optional): set on every file, like video-id. A transcoder copies them to what it writes and sends them in its message.</Text>
            {tags.map((t, i) => (
              <FormRow key={i}>
                <Input aria-label={`Tag ${i + 1} name`} mono value={t.name} onChange={(e) => setTags(tags.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)))} placeholder="video-id" spellCheck={false} />
                <Input aria-label={`Tag ${i + 1} value`} mono value={t.value} onChange={(e) => setTags(tags.map((x, j) => (j === i ? { ...x, value: e.target.value } : x)))} placeholder="v42" spellCheck={false} />
                <IconButton label={`Remove tag ${i + 1}`} icon={<Icons.X />} variant="ghost" onClick={() => setTags(tags.filter((_, j) => j !== i))} />
                {tagErrors[`tags.${t.name.trim()}`] && <FieldError>{tagErrors[`tags.${t.name.trim()}`]}</FieldError>}
              </FormRow>
            ))}
            <div><Button size="sm" variant="ghost" icon={<Icons.Plus />} onClick={() => setTags([...tags, { name: '', value: '' }])}>Add tag</Button></div>
          </Stack>
        </Stack>
      } />
  );
}

/** Folders exist only through their files: this opens an empty one to upload into. */
function NewFolderDialog({ prefix, onClose, onOpen }: { prefix: string; onClose: () => void; onOpen: (prefix: string) => void }) {
  const [name, setName] = useState('');
  const [error, setError] = useState<string | null>(null);
  return (
    <ConfirmDialog title="New folder" confirmLabel="Open folder" error={error ?? undefined} onCancel={onClose}
      onConfirm={() => {
        const p = `${prefix}${name.trim().replace(/^\/+|\/+$/g, '')}`;
        const bad = validateObjectKey(p);
        if (bad) return setError(bad);
        onOpen(`${p}/`);
      }}
      body={
        <Field id="f-folder-name" label="Name" hint={`Inside ${prefix || 'the top of the bucket'}. It appears once you upload a file into it.`}>
          <Input id="f-folder-name" mono value={name} onChange={(e) => setName(e.target.value)} placeholder="incoming" spellCheck={false} />
        </Field>
      } />
  );
}
