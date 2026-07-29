import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from './App';
import { setToken } from './auth';
import type { ConfigObject } from './api';

const artifact = (over: Partial<ConfigObject> = {}): ConfigObject => ({
  cfg_id: 'ROOT',
  artifact: 'Draft-Policy.md',
  version: '1.0.0',
  role: 'governance',
  lifecycle: 'draft',
  retention_class: 'working_material',
  authority: 'non_normative',
  row_version: 3,
  created_at: '2026-07-24T12:00:00Z',
  updated_at: '2026-07-24T12:00:00Z',
  ...over,
});

const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });
const fail = (status: number, body: Record<string, unknown>) => ({
  ok: false,
  status,
  json: async () => ({ status, ...body }),
});

/** Routes by URL; `ratify` controls the outcome of the governance call. */
function mock(opts: { obj?: Partial<ConfigObject>; ratify?: unknown } = {}) {
  return vi.fn().mockImplementation(async (url: string, init?: RequestInit) => {
    const u = String(url);
    if (u.includes('/auth/config')) return ok({ auth_enabled: false });
    if (u.includes('/ratify')) {
      if (opts.ratify && typeof opts.ratify === 'object' && 'ok' in (opts.ratify as object)) {
        return opts.ratify;
      }
      return ok({
        operation: 'ratification',
        cfg_id: 'ROOT',
        actor: 'priya@partner.example',
        occurred_at: '2026-07-24T13:00:00Z',
        row_version: 4,
        state: { lifecycle: 'ratified', retention_class: 'current_baseline', authority: 'active' },
        audit: { operation_id: String(new Headers(init?.headers).get('Idempotency-Key')), chain_id: 'ROOT', recorded: true },
      });
    }
    if (u.includes('/history')) return ok({ items: [] });
    if (u.includes('/audit')) return ok({ chain_id: 'ROOT', verified: true, checked: 0, head_seq: 0 });
    if (u.includes('/dependencies') || u.includes('/dependents')) {
      return ok({ root: 'ROOT', direction: 'dependencies', count: 0, nodes: [] });
    }
    if (u.includes('/v1/artifacts/ROOT')) return ok(artifact(opts.obj));
    return ok({ items: [artifact(opts.obj)] });
  });
}

const openDialog = async () => {
  fireEvent.click(await screen.findByRole('button', { name: 'Ratify' }));
  return screen.findByRole('dialog');
};

/** Confirms inside the dialog — the header button shares the same label. */
const confirm = async () => {
  const dialog = await screen.findByRole('dialog');
  fireEvent.click(within(dialog).getByRole('button', { name: 'Ratify' }));
};

beforeEach(() => {
  window.history.replaceState(null, '', '/?artifact=ROOT');
  setToken(null);
});
afterEach(() => vi.unstubAllGlobals());

describe('ratify workflow', () => {
  it('offers Ratify on a draft artifact', async () => {
    vi.stubGlobal('fetch', mock());
    render(<App />);
    expect(await screen.findByRole('button', { name: 'Ratify' })).toBeInTheDocument();
  });

  it('withholds Ratify when the precondition does not hold, and says why', async () => {
    vi.stubGlobal('fetch', mock({ obj: { lifecycle: 'ratified' } }));
    render(<App />);

    expect(await screen.findByText('Ratify unavailable')).toBeInTheDocument();
    expect(screen.getByText('Ratify unavailable')).toHaveAttribute(
      'title',
      expect.stringContaining('requires a draft or in-review'),
    );
    expect(screen.queryByRole('button', { name: 'Ratify' })).not.toBeInTheDocument();
  });

  it('states the effect before the user commits', async () => {
    vi.stubGlobal('fetch', mock());
    render(<App />);
    const dialog = await openDialog();

    expect(dialog).toHaveTextContent(/Lifecycle → Ratified/);
    expect(dialog).toHaveTextContent(/Authority → Active/);
  });

  it('sends Idempotency-Key and If-Match, and reports the new state', async () => {
    const f = mock();
    vi.stubGlobal('fetch', f);
    render(<App />);
    await openDialog();

    await confirm();

    expect(await screen.findByRole('status')).toHaveTextContent(/now governs/i);

    const call = f.mock.calls.map((c) => c as [string, RequestInit]).find(([u]) => String(u).includes('/ratify'))!;
    const headers = new Headers(call[1].headers);
    expect(call[1].method).toBe('POST');
    expect(headers.get('Idempotency-Key')).toBeTruthy();
    expect(headers.get('If-Match')).toBe('"3"'); // the artifact's row_version
  });

  it('reuses the same idempotency key when a failed attempt is retried', async () => {
    const f = mock({ ratify: fail(500, { title: 'internal error' }) });
    vi.stubGlobal('fetch', f);
    render(<App />);
    await openDialog();

    await confirm();
    await screen.findByRole('alert');
    await confirm();

    const keys = f.mock.calls
      .map((c) => c as [string, RequestInit])
      .filter(([u]) => String(u).includes('/ratify'))
      .map(([, init]) => new Headers(init.headers).get('Idempotency-Key'));

    await waitFor(() => expect(keys.length).toBeGreaterThan(1));
    expect(new Set(keys).size).toBe(1);
  });

  it.each([
    [403, 'You do not have permission'],
    [409, 'The operation is not permitted'],
    [412, 'This artifact changed while you were viewing it'],
    [428, 'A precondition was missing'],
    [500, 'OneOps could not complete the operation'],
  ])('explains a %i failure to the user', async (status, expected) => {
    vi.stubGlobal('fetch', mock({ ratify: fail(status, { title: 'x', detail: undefined }) }));
    render(<App />);
    await openDialog();

    await confirm();

    expect(await screen.findByRole('alert')).toHaveTextContent(expected);
  });

  it('prefers the server’s own reason on a 409', async () => {
    vi.stubGlobal(
      'fetch',
      mock({ ratify: fail(409, { title: 'conflict', detail: 'ratification requires draft or in_review' }) }),
    );
    render(<App />);
    await openDialog();
    await confirm();

    expect(await screen.findByRole('alert')).toHaveTextContent(/requires draft or in_review/);
  });

  it('closes on Escape without performing the operation', async () => {
    const f = mock();
    vi.stubGlobal('fetch', f);
    render(<App />);
    await openDialog();

    fireEvent.keyDown(window, { key: 'Escape' });

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(f.mock.calls.filter(([u]) => String(u).includes('/ratify'))).toHaveLength(0);
  });
});
