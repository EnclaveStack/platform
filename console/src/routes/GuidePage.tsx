import { Bullets, buttonClass, CodeBlock, GuidePart, Icons, Inline, Page, PageHeader, Stack, Steps, Text } from '@enclavestack/ui';
import { Link, Navigate, useParams } from 'react-router';
import { GUIDES, websiteGuideUrl } from '../lib/guides';
import { inSentence, sectionBySlug } from '../lib/sections';
import { usePageTitle } from '../lib/shell';

// A section's guide (CON-67, L2.5-002 §16): five colour-coded parts, related guides, then the full guide on the website.

export function GuidePage() {
  const { section: slug } = useParams();
  const section = sectionBySlug(slug === 'records' ? 'zones' : slug);
  const key = section && section.slug !== 'records' ? section.slug : undefined;
  const guide = key ? GUIDES[key] : undefined;
  usePageTitle(section ? 'Guide' : undefined);
  if (!section || !key || !guide) return <Navigate to="/" replace />;

  return (
    <Page>
      <PageHeader
        label="Guide"
        back={<Link to={`/${section.slug}`} className={buttonClass({ variant: 'ghost', size: 'sm' })}><Icons.ChevronLeft /> {section.label}</Link>}
        title={`Getting started with ${inSentence(section.label)}`}
        lede={section.blurb}
        actions={<Link to={`/${section.slug}`} className={buttonClass({ variant: 'dark' })}>Go to {inSentence(section.label)}</Link>}
      />
      <Stack gap={4}>
        <GuidePart tone="what" title="What it is">
          {guide.what.map((p) => <Text key={p} as="p">{p}</Text>)}
        </GuidePart>
        <GuidePart tone="when" title="When to use it">
          <Bullets>{guide.when.map((w) => <li key={w}>{w}</li>)}</Bullets>
        </GuidePart>
        <GuidePart tone="how" title="How to use it">
          <Steps>{guide.how.map((s) => <li key={s}>{s}</li>)}</Steps>
          <CodeBlock label="The same from the command line" code={guide.command} />
        </GuidePart>
        <GuidePart tone="example" title="Example">
          {guide.example.map((p) => <Text key={p} as="p">{p}</Text>)}
        </GuidePart>
        <GuidePart tone="note" title="Good to know">
          <Bullets>{guide.note.map((n) => <li key={n}>{n}</li>)}</Bullets>
        </GuidePart>
        <Stack gap={2}>
          <Text weight="semibold">Related guides</Text>
          <Inline gap={2} wrap>
            {guide.related.map((r) => {
              const s = sectionBySlug(r);
              return s ? <Link key={r} to={`/guide/${r}`} className={buttonClass({ size: 'sm' })}>{s.label}</Link> : null;
            })}
          </Inline>
        </Stack>
        <Stack gap={2}>
          <Text weight="semibold">Want more detail?</Text>
          <Text tone="muted">The full guide on the EnclaveStack website covers every setting, the API and OpenTofu, with more examples. It describes the latest release.</Text>
          <Inline gap={2} wrap>
            <a href={websiteGuideUrl(key)} target="_blank" rel="noreferrer" className={buttonClass({ size: 'sm' })}><Icons.ExternalLink /> Full {inSentence(section.label)} guide on enclavestack.soupler.com</a>
          </Inline>
        </Stack>
      </Stack>
    </Page>
  );
}
