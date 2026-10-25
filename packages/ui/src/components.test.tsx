import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { useState } from 'react';
import {
  Avatar, avatarTone, Button, buttonClass, Choice, CodeBlock, ConfirmDialog, Drawer, Field, IconButton, initialsOf, Input, MenuSelect, ProgressBar,
  SegmentedTabs, StageTrack, Status, ToastProvider, useToast,
} from './index';

describe('Button', () => {
  it('defaults to type=button so it never submits a form by accident', () => {
    render(<Button>Save</Button>);
    expect(screen.getByRole('button', { name: 'Save' })).toHaveAttribute('type', 'button');
  });

  it('maps variant and size to design-system classes', () => {
    expect(buttonClass({ variant: 'dark', size: 'sm', block: true })).toBe('es-btn es-btn-dark es-btn-sm es-btn-block');
    expect(buttonClass()).toBe('es-btn es-btn-outline');
  });

  it('icon buttons are named by their label', () => {
    render(<IconButton label="Close" icon={<svg />} />);
    expect(screen.getByRole('button', { name: 'Close' })).toBeInTheDocument();
  });
});

describe('Choice', () => {
  it('is a radio named by its title, checked by clicking anywhere on the card', async () => {
    function Group() {
      const [v, setV] = useState('new');
      return (
        <>
          <Choice name="mode" value="new" title="Into a new database" description="Recommended" checked={v === 'new'} onChange={() => setV('new')} />
          <Choice name="mode" value="replace" title="Replace its data" checked={v === 'replace'} onChange={() => setV('replace')} />
        </>
      );
    }
    render(<Group />);
    expect(screen.getByRole('radio', { name: /Into a new database/ })).toBeChecked();
    await userEvent.click(screen.getByText('Replace its data'));
    expect(screen.getByRole('radio', { name: 'Replace its data' })).toBeChecked();
    expect(screen.getByRole('radio', { name: /Into a new database/ })).not.toBeChecked();
  });
});

describe('Avatar', () => {
  it('derives stable initials and tone from the name', () => {
    expect(initialsOf('orders-db')).toBe('OD');
    expect(initialsOf('ada@example.dev')).toBe('AD');
    expect(initialsOf('jane.doe@example.com')).toBe('JD');
    expect(initialsOf('')).toBe('?');
    expect(avatarTone('web')).toBe(avatarTone('web'));
    expect(avatarTone('web')).toBeGreaterThanOrEqual(1);
    expect(avatarTone('web')).toBeLessThanOrEqual(6);
  });

  it('is decorative unless labelled', () => {
    const { container } = render(<Avatar name="web" />);
    expect(container.firstElementChild).toHaveAttribute('aria-hidden', 'true');
  });
});

describe('Status and progress', () => {
  it('status shows a word, never colour alone', () => {
    render(<Status tone="ok">Ready</Status>);
    expect(screen.getByText('Ready')).toBeInTheDocument();
  });

  it('progress clamps and is announced as a progress bar', () => {
    render(<ProgressBar label="Memory" value={140} />);
    expect(screen.getByRole('progressbar', { name: 'Memory' })).toHaveAttribute('value', '100');
  });
});

describe('Field', () => {
  it('labels its control and swaps the hint for the error', () => {
    const { rerender } = render(<Field id="n" label="Name" hint="Lowercase"><Input id="n" /></Field>);
    expect(screen.getByLabelText('Name')).toBeInTheDocument();
    expect(screen.getByText('Lowercase')).toBeInTheDocument();
    rerender(<Field id="n" label="Name" hint="Lowercase" error="Too short"><Input id="n" /></Field>);
    expect(screen.queryByText('Lowercase')).not.toBeInTheDocument();
    expect(screen.getByText('Too short')).toBeInTheDocument();
  });
});

describe('SegmentedTabs', () => {
  function Harness() {
    const [tab, setTab] = useState<'a' | 'b' | 'c'>('a');
    return <SegmentedTabs label="Details" selected={tab} onSelect={setTab} tabs={[{ id: 'a', label: 'One' }, { id: 'b', label: 'Two' }, { id: 'c', label: 'Three', count: 4 }]} />;
  }

  it('selects on click and moves with arrow keys, wrapping around', async () => {
    const user = userEvent.setup();
    render(<Harness />);
    await user.click(screen.getByRole('tab', { name: 'Two' }));
    expect(screen.getByRole('tab', { name: 'Two' })).toHaveAttribute('aria-selected', 'true');
    await user.keyboard('{ArrowRight}');
    expect(screen.getByRole('tab', { name: /Three/ })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByRole('tab', { name: /Three/ })).toHaveFocus();
    await user.keyboard('{ArrowRight}');
    expect(screen.getByRole('tab', { name: 'One' })).toHaveAttribute('aria-selected', 'true');
    await user.keyboard('{ArrowLeft}');
    expect(screen.getByRole('tab', { name: /Three/ })).toHaveAttribute('aria-selected', 'true');
  });
});

describe('StageTrack', () => {
  it('numbers pending steps, marks the active one and reads states aloud', () => {
    render(<StageTrack label="Progress" stages={[{ label: 'Pull', state: 'done' }, { label: 'Start', state: 'active' }, { label: 'Check', state: 'pending' }]} />);
    const list = screen.getByRole('list', { name: 'Progress' });
    expect(list).toHaveClass('es-steps-3');
    expect(screen.getByText('Start').closest('li')).toHaveAttribute('aria-current', 'step');
    expect(screen.getByText('3')).toBeInTheDocument();
    expect(screen.getByText(/— done/)).toBeInTheDocument();
  });
});

describe('ConfirmDialog', () => {
  it('holds the confirm button until the name is typed, and Escape cancels', async () => {
    const user = userEvent.setup();
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(<ConfirmDialog title="Delete web?" body="Gone for good." confirmLabel="Delete app" typeToConfirm="web" danger onConfirm={onConfirm} onCancel={onCancel} />);
    const confirm = screen.getByRole('button', { name: 'Delete app' });
    expect(confirm).toBeDisabled();
    await user.type(screen.getByLabelText(/Type/), 'web');
    expect(confirm).toBeEnabled();
    await user.click(confirm);
    expect(onConfirm).toHaveBeenCalledOnce();
    await user.keyboard('{Escape}');
    expect(onCancel).toHaveBeenCalled();
  });
});

describe('Drawer', () => {
  it('is a named modal dialog that focuses its first field and closes', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(<Drawer title="New app" onClose={onClose}><Input aria-label="Name" /></Drawer>);
    expect(screen.getByRole('dialog', { name: 'New app' })).toBeInTheDocument();
    expect(screen.getByLabelText('Name')).toHaveFocus();
    await user.click(screen.getAllByRole('button', { name: 'Close' })[1]!);
    expect(onClose).toHaveBeenCalledOnce();
  });
});

describe('CodeBlock', () => {
  it('copies and confirms, then falls back when the clipboard is blocked', async () => {
    const user = userEvent.setup();
    render(<CodeBlock label="CLI" code="enclave app list" />);
    expect(screen.getByLabelText('CLI')).toHaveTextContent('enclave app list');
    await user.click(screen.getByRole('button', { name: 'Copy CLI' }));
    expect(await screen.findByText('Copied')).toBeInTheDocument();
    vi.spyOn(navigator.clipboard, 'writeText').mockRejectedValueOnce(new Error('blocked'));
    await user.click(screen.getByRole('button', { name: 'Copy CLI' }));
    expect(await screen.findByText('Select it')).toBeInTheDocument();
  });
});

describe('Toasts', () => {
  function Trigger() {
    const toast = useToast();
    return <Button onClick={() => toast('Saved', { label: 'Undo', run: () => toast('Undone') })}>Go</Button>;
  }

  it('shows a message with its action and expires', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const user = userEvent.setup({ advanceTimers: vi.advanceTimersByTime });
    render(<ToastProvider><Trigger /></ToastProvider>);
    await user.click(screen.getByRole('button', { name: 'Go' }));
    expect(screen.getByText('Saved')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Undo' }));
    expect(screen.queryByText('Saved')).not.toBeInTheDocument();
    expect(screen.getByText('Undone')).toBeInTheDocument();
    act(() => vi.advanceTimersByTime(6500));
    expect(screen.queryByText('Undone')).not.toBeInTheDocument();
    vi.useRealTimers();
  });

  it('useToast outside a provider fails loudly', () => {
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    expect(() => render(<Trigger />)).toThrow(/ToastProvider/);
  });
});

describe('MenuSelect', () => {
  const options = [{ value: '', label: 'All projects' }, { value: 'default', label: 'default' }, { value: 'shop', label: 'shop' }];
  function Menu() {
    const [v, setV] = useState('');
    return <><MenuSelect label="Project" value={v} options={options} onChange={setV} /><p>chosen:{v || 'none'}</p></>;
  }

  it('shows the current choice, opens a list with it ticked and picks by click', async () => {
    const user = userEvent.setup();
    render(<Menu />);
    const button = screen.getByRole('button', { name: 'Project: All projects' });
    expect(button).toHaveAttribute('aria-expanded', 'false');
    await user.click(button);
    expect(screen.getByRole('option', { name: 'All projects' })).toHaveAttribute('aria-selected', 'true');
    await user.click(screen.getByRole('option', { name: 'shop' }));
    expect(screen.getByText('chosen:shop')).toBeInTheDocument();
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Project: shop' })).toHaveFocus();
  });

  it('works from the keyboard: arrows move, Enter picks, Escape closes without picking', async () => {
    const user = userEvent.setup();
    render(<Menu />);
    await user.click(screen.getByRole('button', { name: /^Project:/ }));
    await user.keyboard('{ArrowDown}{ArrowDown}{Enter}');
    expect(screen.getByText('chosen:shop')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /^Project:/ }));
    await user.keyboard('{Home}{Escape}');
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
    expect(screen.getByText('chosen:shop')).toBeInTheDocument();
  });

  it('closes on a click outside', async () => {
    const user = userEvent.setup();
    render(<Menu />);
    await user.click(screen.getByRole('button', { name: /^Project:/ }));
    await user.click(screen.getByText('chosen:none'));
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  });
});
