import { AuthLayout as Layout, Avatar, Chip, Icons, Inline, MachineCard, Stack, Status, Text } from '@enclavestack/ui';
import type { ReactNode } from 'react';

const KINDS = ['Apps', 'Databases', 'Caches', 'Buckets', 'Secrets'];

/** Sign-in and setup share this split layout: the product on the left, the form on the right. */
export function AuthLayout({ title, lede, children }: { title: string; lede: string; children: ReactNode }) {
  return (
    <Layout
      title={title}
      lede={lede}
      aside={
        <Stack gap={3} aria-hidden="true">
          <MachineCard>
            <Avatar name="machine" size="md"><Icons.Server /></Avatar>
            <Stack gap={0} grow>
              <Text weight="bold">Your machine</Text>
              <Text size="xs" tone="subtle" mono>enclave.local</Text>
            </Stack>
            <Status tone="ok">Online</Status>
          </MachineCard>
          <Inline gap={2} wrap>
            {KINDS.map((k) => <Chip key={k}>{k}</Chip>)}
          </Inline>
        </Stack>
      }
    >
      {children}
    </Layout>
  );
}
