import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from './App';
import type { ConfigObject } from './api';

const artifact = (over: Partial<ConfigObject> = {}): ConfigObject => ({
  cfg_id: 'ROOT',
  artifact: 'OneOps-Constitution-Volume-I.md',
  version: '1.0.0',
  role: 'constitution',
  lifecycle: 'ratified',
  retention_class: 'current_baseline',
  authority: 'active',
  row_version: 3,
  created_at: '2026-07-24T12:00:00Z',
  updated_at: '2026-07-24T12:00:00Z',
  ...over,
});

/** Routes by URL so one mock serves list, detail and both graph directions. */
function routedFetch(over: Record<string, unknown> = {}) {
  return vi.fn().mockImplementation(async (url: string) => {
    const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });

    for (const [frag, body] of Object.entries(over)) {
      if (url.includes(frag)) {
        if (body === 'ERROR') return { ok: false, status: 500, json: async () => ({ title: 'boom', status: 500 }) };
        return ok(body);
      }
    }
    if (url.includes('/history')) return ok({ items: [] });
    if (url.includes('/audit')) return ok({ chain_id: 'ROOT', verified: true, checked: 0, head_seq: 0 });
    if (url.includes('/dependencies')) {
      return ok({ root: 'ROOT', direction: 'dependencies', count: 1, nodes: [{ cfg_id: 'DEP1', depth: 1 }] });
    }
    if (url.includes('/dependents')) {
      return ok({ root: 'ROOT', direction: 'dependents', count: 0, nodes: [] });
    }
    if (url.includes('/v1/artifacts/DEP1')) {
      return ok(artifact({ cfg_id: 'DEP1', artifact: 'Configuration-State-Model.md', authority: 'historical' }));
    }
    if (url.includes('/v1/artifacts/ROOT')) return ok(artifact());
    return ok({ items: [artifact()] });
  });
}

beforeEach(() => window.history.replaceState(null, '', '/'));
afterEach(() => vi.unstubAllGlobals());

describe('artifact detail', () => {
  it('opens from the estate list and puts the id in the URL', async () => {
    vi.stubGlobal('fetch', routedFetch());
    render(<App />);

    fireEvent.click(await screen.findByRole('button', { name: /Constitution-Volume-I/ }));

    expect(await screen.findByRole('heading', { name: /Constitution-Volume-I/ })).toBeInTheDocument();
    expect(window.location.search).toContain('artifact=ROOT');
  });

  it('shows all four classification dimensions', async () => {
    vi.stubGlobal('fetch', routedFetch());
    window.history.replaceState(null, '', '/?artifact=ROOT');
    render(<App />);

    const panel = (await screen.findByRole('heading', { name: 'Classification' })).closest('section')!;
    expect(within(panel).getByText('Authority')).toBeInTheDocument();
    expect(within(panel).getByText('Lifecycle')).toBeInTheDocument();
    expect(within(panel).getByText('Retention')).toBeInTheDocument();
    expect(within(panel).getByText('Role')).toBeInTheDocument();
    expect(within(panel).getByText('Current baseline')).toBeInTheDocument();
  });

  it('resolves relation names, since the graph returns ids only', async () => {
    vi.stubGlobal('fetch', routedFetch());
    window.history.replaceState(null, '', '/?artifact=ROOT');
    render(<App />);

    expect(await screen.findByText('Configuration-State-Model.md')).toBeInTheDocument();
  });

  it('still renders the artifact when the graph is unavailable', async () => {
    vi.stubGlobal('fetch', routedFetch({ '/dependencies': 'ERROR', '/dependents': 'ERROR' }));
    window.history.replaceState(null, '', '/?artifact=ROOT');
    render(<App />);

    expect(await screen.findByRole('heading', { name: /Constitution-Volume-I/ })).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
  });

  it('surfaces the problem detail when the artifact itself fails', async () => {
    vi.stubGlobal('fetch', routedFetch({ '/v1/artifacts/ROOT': 'ERROR' }));
    window.history.replaceState(null, '', '/?artifact=ROOT');
    render(<App />);

    expect(await screen.findByRole('alert')).toBeInTheDocument();
  });

  it('returns to the estate and clears the id from the URL', async () => {
    vi.stubGlobal('fetch', routedFetch());
    window.history.replaceState(null, '', '/?artifact=ROOT');
    render(<App />);

    fireEvent.click(await screen.findByRole('button', { name: /Back to estate/ }));

    await waitFor(() => expect(window.location.search).not.toContain('artifact'));
    expect(await screen.findByRole('table')).toBeInTheDocument();
  });
});
