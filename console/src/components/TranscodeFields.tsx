import { Button, Card, Checkbox, Field, FieldError, FieldHint, Fieldset, FormRow, Icons, IconButton, Input, Row, Rows, Select, Stack, Text } from '@enclavestack/ui';
import { useState, type ReactNode } from 'react';
import { RULES } from '../api/generated';
import type { HLSLadder, Rendition, RenditionContainer, TranscodeNotify, TranscoderSpec } from '../api/types';
import { masterPlaylist } from '../lib/transcode';

// Renditions, the HLS ladder and "notify when a file is done" of a transcoder (L2-012 CON-72, CON-73).

interface BoxRow {
  name: string;
  width: string;
  height: string;
  kbps: string;
}
interface RenditionRow extends BoxRow {
  container: RenditionContainer;
  audio: string;
  gop: string;
}

const num = (s: string) => (s.trim() === '' ? undefined : Number(s));
const CONTAINER_LABEL: Record<RenditionContainer, string> = { mp4: 'MP4 (H.264, AAC)', webm: 'WebM (VP9, Opus)' };

/** A portrait ladder that suits phones, offered when the ladder is turned on. */
const STARTER_RUNGS: BoxRow[] = [
  { name: '360p', width: '360', height: '640', kbps: '600' },
  { name: '720p', width: '720', height: '1280', kbps: '2000' },
  { name: '1080p', width: '1080', height: '1920', kbps: '4000' },
];

/** The form state of renditions, the ladder and notify; `build` returns their spec fields. */
export function useTranscodeOutputs(tc: Partial<TranscoderSpec>) {
  const [renditions, setRenditions] = useState<RenditionRow[]>((tc.renditions ?? []).map((r) => ({
    name: r.name, container: r.container, width: String(r.maxWidth), height: String(r.maxHeight), kbps: String(r.videoKbps),
    audio: String(r.audioKbps ?? RULES.audioKbpsDefault), gop: String(r.gopSeconds ?? RULES.gopSecondsDefault),
  })));
  const [hlsOn, setHlsOn] = useState(Boolean(tc.hls));
  const [hlsName, setHlsName] = useState(tc.hls?.name ?? RULES.hlsNameDefault);
  const [segment, setSegment] = useState(String(tc.hls?.segmentSeconds ?? RULES.segmentSecondsDefault));
  const [gop, setGop] = useState(String(tc.hls?.gopSeconds ?? RULES.gopSecondsDefault));
  const [hlsAudio, setHlsAudio] = useState(String(tc.hls?.audioKbps ?? RULES.audioKbpsDefault));
  const [rungs, setRungs] = useState<BoxRow[]>(tc.hls ? tc.hls.rungs.map((r) => ({ name: r.name, width: String(r.maxWidth), height: String(r.maxHeight), kbps: String(r.videoKbps) })) : STARTER_RUNGS);
  const [webhook, setWebhook] = useState(tc.notify?.webhook?.url ?? '');
  const [secret, setSecret] = useState(tc.notify?.webhook?.secret ?? '');
  const [topic, setTopic] = useState(tc.notify?.topic ?? '');

  function build(): Pick<TranscoderSpec, 'renditions' | 'hls' | 'notify'> {
    const out: Pick<TranscoderSpec, 'renditions' | 'hls' | 'notify'> = {};
    if (renditions.length) out.renditions = renditions.map((r) => ({ name: r.name.trim(), container: r.container, maxWidth: Number(r.width), maxHeight: Number(r.height),
      videoKbps: Number(r.kbps), audioKbps: num(r.audio), gopSeconds: num(r.gop) }));
    if (hlsOn) out.hls = { name: hlsName.trim() || undefined, segmentSeconds: num(segment), gopSeconds: num(gop), audioKbps: num(hlsAudio),
      rungs: rungs.map((r) => ({ name: r.name.trim(), maxWidth: Number(r.width), maxHeight: Number(r.height), videoKbps: Number(r.kbps) })) };
    if (webhook.trim() || topic) {
      const n: TranscodeNotify = {};
      if (webhook.trim()) n.webhook = { url: webhook.trim(), ...(secret ? { secret } : {}) };
      if (topic) n.topic = topic;
      out.notify = n;
    }
    return out;
  }

  function render({ errors, secrets, topics, onBlur }: { errors: Record<string, string>; secrets: string[]; topics: string[]; onBlur: () => void }): ReactNode {
    const err = (f: string) => errors[f];
    const boxFields = (f: string, i: number, label: string, row: BoxRow, set: (p: Partial<BoxRow>) => void) => (
      <>
        <Input aria-label={`${label} ${i + 1} name`} mono value={row.name} onChange={(e) => set({ name: e.target.value })} onBlur={onBlur} placeholder="720p" spellCheck={false} aria-invalid={Boolean(err(`${f}.name`))} />
        <Input aria-label={`${label} ${i + 1} width`} inputMode="numeric" value={row.width} onChange={(e) => set({ width: e.target.value })} onBlur={onBlur} aria-invalid={Boolean(err(`${f}.maxWidth`))} />
        <Input aria-label={`${label} ${i + 1} height`} inputMode="numeric" value={row.height} onChange={(e) => set({ height: e.target.value })} onBlur={onBlur} aria-invalid={Boolean(err(`${f}.maxHeight`))} />
        <Input aria-label={`${label} ${i + 1} video kbps`} inputMode="numeric" value={row.kbps} onChange={(e) => set({ kbps: e.target.value })} onBlur={onBlur} aria-invalid={Boolean(err(`${f}.videoKbps`))} />
      </>
    );
    const rowError = (f: string) => ['name', 'container', 'maxWidth', 'maxHeight', 'videoKbps', 'audioKbps', 'gopSeconds'].map((k) => err(`${f}.${k}`)).find(Boolean);
    return (
      <>
        <Fieldset legend="Renditions">
          <FieldHint>Files of your own size and bitrate: each fits inside width × height, keeps its shape and is never made larger than the original.</FieldHint>
          {renditions.map((row, i) => {
            const f = `spec.renditions[${i}]`;
            const set = (p: Partial<RenditionRow>) => setRenditions(renditions.map((x, j) => (j === i ? { ...x, ...p } : x)));
            const e = rowError(f);
            return (
              <Stack key={i} gap={2}>
                <FormRow>
                  {boxFields(f, i, 'Rendition', row, set)}
                  <Select aria-label={`Rendition ${i + 1} format`} value={row.container} onChange={(e) => set({ container: e.target.value as RenditionContainer })}>
                    {(RULES.renditionContainers as readonly RenditionContainer[]).map((c) => (
                      <option key={c} value={c}>{CONTAINER_LABEL[c]}</option>
                    ))}
                  </Select>
                  <IconButton label={`Remove rendition ${i + 1}`} icon={<Icons.X />} variant="ghost" onClick={() => setRenditions(renditions.filter((_, j) => j !== i))} />
                </FormRow>
                <FormRow>
                  <Field id={`f-rend-audio-${i}`} label="Audio kbps" hint={`${RULES.audioKbpsMin}–${RULES.audioKbpsMax}.`}>
                    <Input id={`f-rend-audio-${i}`} inputMode="numeric" value={row.audio} onChange={(e) => set({ audio: e.target.value })} onBlur={onBlur} />
                  </Field>
                  <Field id={`f-rend-gop-${i}`} label="Keyframe every (s)" hint={`${RULES.gopSecondsMin}–${RULES.gopSecondsMax}.`}>
                    <Input id={`f-rend-gop-${i}`} inputMode="numeric" value={row.gop} onChange={(e) => set({ gop: e.target.value })} onBlur={onBlur} />
                  </Field>
                </FormRow>
                {e && <FieldError>{e}</FieldError>}
              </Stack>
            );
          })}
          {err('spec.renditions') && <FieldError>{err('spec.renditions')}</FieldError>}
          {renditions.length < RULES.renditionsMax && (
            <div>
              <Button variant="ghost" size="sm" icon={<Icons.Plus />} onClick={() => setRenditions([...renditions, { name: '', container: 'mp4', width: '1280', height: '720', kbps: '2500',
                audio: String(RULES.audioKbpsDefault), gop: String(RULES.gopSecondsDefault) }])}>Add rendition</Button>
            </div>
          )}
        </Fieldset>
        <Fieldset legend="HLS ladder">
          <Checkbox label="Make an adaptive HLS stream (players switch quality as the connection changes)" checked={hlsOn} onChange={(e) => setHlsOn(e.target.checked)} />
          {hlsOn && (
            <Stack gap={3}>
              <FormRow>
                <Field id="f-hls-name" label="Folder" hint={`${hlsName || RULES.hlsNameDefault}/master.m3u8 is what players load.`} error={err('spec.hls.name')}>
                  <Input id="f-hls-name" mono value={hlsName} onChange={(e) => setHlsName(e.target.value)} onBlur={onBlur} spellCheck={false} />
                </Field>
                <Field id="f-hls-segment" label="Segment length (s)" hint="A multiple of the keyframe interval." error={err('spec.hls.segmentSeconds')}>
                  <Input id="f-hls-segment" inputMode="numeric" value={segment} onChange={(e) => setSegment(e.target.value)} onBlur={onBlur} />
                </Field>
                <Field id="f-hls-gop" label="Keyframe every (s)" hint="The same in every rung, so they line up." error={err('spec.hls.gopSeconds')}>
                  <Input id="f-hls-gop" inputMode="numeric" value={gop} onChange={(e) => setGop(e.target.value)} onBlur={onBlur} />
                </Field>
                <Field id="f-hls-audio" label="Audio kbps" error={err('spec.hls.audioKbps')}>
                  <Input id="f-hls-audio" inputMode="numeric" value={hlsAudio} onChange={(e) => setHlsAudio(e.target.value)} onBlur={onBlur} />
                </Field>
              </FormRow>
              <FieldHint>Rungs: a name, the largest width × height and the video bitrate in kbps. 9:16 sizes suit phone video.</FieldHint>
              {rungs.map((row, i) => {
                const f = `spec.hls.rungs[${i}]`;
                const e = rowError(f);
                return (
                  <Stack key={i} gap={1}>
                    <FormRow>
                      {boxFields(f, i, 'Rung', row, (p) => setRungs(rungs.map((x, j) => (j === i ? { ...x, ...p } : x))))}
                      <IconButton label={`Remove rung ${i + 1}`} icon={<Icons.X />} variant="ghost" onClick={() => setRungs(rungs.filter((_, j) => j !== i))} />
                    </FormRow>
                    {e && <FieldError>{e}</FieldError>}
                  </Stack>
                );
              })}
              {err('spec.hls.rungs') && <FieldError>{err('spec.hls.rungs')}</FieldError>}
              {rungs.length < RULES.hlsRungsMax && (
                <div>
                  <Button variant="ghost" size="sm" icon={<Icons.Plus />} onClick={() => setRungs([...rungs, { name: '', width: '', height: '', kbps: '' }])}>Add rung</Button>
                </div>
              )}
            </Stack>
          )}
        </Fieldset>
        <Fieldset legend="Notify when a file is done">
          <FieldHint>A JSON message for every file that finishes, fails or times out: its status, the files written, the master playlist and the uploader's tags.</FieldHint>
          {err('spec.notify') && <FieldError>{err('spec.notify')}</FieldError>}
          <FormRow>
            <Field id="f-notify-url" label="Webhook URL" hint="https://, or http:// to localhost. Tried 3 times." error={err('spec.notify.webhook.url')}>
              <Input id="f-notify-url" mono value={webhook} onChange={(e) => setWebhook(e.target.value)} onBlur={onBlur} placeholder="https://video.example.com/hooks/transcode" spellCheck={false} />
            </Field>
            <Field id="f-notify-secret" label="Signing secret" hint="Signs each message (X-Enclave-Signature)." error={err('spec.notify.webhook.secret')}>
              <Select id="f-notify-secret" value={secret} onChange={(e) => setSecret(e.target.value)} disabled={!webhook.trim()}>
                <option value="">Don't sign</option>
                {secrets.map((n) => (
                  <option key={n} value={n}>{n}</option>
                ))}
              </Select>
            </Field>
            <Field id="f-notify-topic" label="Topic" hint="Publish the same message to a topic." error={err('spec.notify.topic')}>
              <Select id="f-notify-topic" value={topic} onChange={(e) => setTopic(e.target.value)}>
                <option value="">None</option>
                {topics.map((n) => (
                  <option key={n} value={n}>{n}</option>
                ))}
              </Select>
            </Field>
          </FormRow>
        </Fieldset>
      </>
    );
  }

  return { build, render };
}

const boxText = (r: { maxWidth: number; maxHeight: number; videoKbps: number }) => `up to ${r.maxWidth} × ${r.maxHeight} · ${r.videoKbps} kbps`;

/** A transcoder's renditions, ladder and notify settings on its page (CON-73). */
export function TranscodeOutputsCards({ spec }: { spec: TranscoderSpec }) {
  const r: Rendition[] = spec.renditions ?? [];
  const h: HLSLadder | undefined = spec.hls;
  const n = spec.notify;
  return (
    <>
      {r.length > 0 && (
        <Card as="section" title="Renditions">
          <Rows aria-label="Renditions">
            {r.map((x) => (
              <Row key={x.name}>
                <Text mono>{x.name}.{x.container}</Text>
                <Text tone="muted">{boxText(x)} · audio {x.audioKbps ?? RULES.audioKbpsDefault} kbps · keyframe every {x.gopSeconds ?? RULES.gopSecondsDefault} s</Text>
              </Row>
            ))}
          </Rows>
        </Card>
      )}
      {h && (
        <Card as="section" title="HLS ladder">
          <Stack gap={3}>
            <Text tone="muted">
              Players load <Text mono>{masterPlaylist(spec)}</Text> in each file's folder. {h.segmentSeconds ?? RULES.segmentSecondsDefault} s segments, a keyframe every {h.gopSeconds ?? RULES.gopSecondsDefault} s in every rung, audio {h.audioKbps ?? RULES.audioKbpsDefault} kbps.
            </Text>
            <Rows aria-label="Rungs">
              {h.rungs.map((x) => (
                <Row key={x.name}>
                  <Text mono>{x.name}</Text>
                  <Text tone="muted">{boxText(x)}</Text>
                </Row>
              ))}
            </Rows>
          </Stack>
        </Card>
      )}
      <Card as="section" title="Notify">
        {n ? (
          <Rows aria-label="Where messages go">
            {n.webhook && (
              <Row>
                <Text>Webhook</Text>
                <Text mono>{n.webhook.url}</Text>
                <Text tone="muted">{n.webhook.secret ? `signed with ${n.webhook.secret}` : 'not signed'}</Text>
              </Row>
            )}
            {n.topic && (
              <Row>
                <Text>Topic</Text>
                <Text mono>{n.topic}</Text>
              </Row>
            )}
          </Rows>
        ) : (
          <Text tone="muted">No message is sent when a file is done. Edit the transcoder to call a webhook or publish to a topic.</Text>
        )}
      </Card>
    </>
  );
}
