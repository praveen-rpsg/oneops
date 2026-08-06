import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderApp } from '../test-render';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { OnCallNowDTO, OnCallParticipantDTO, OnCallScheduleDTO } from '../onCall';

const schedule = (over: Partial<OnCallScheduleDTO> = {}): OnCallScheduleDTO => ({
  schedule_id: 'SCH-PRIMARY',
  name: 'Primary on-call',
  handoff_interval_seconds: 43_200,
  rotation_start_at: '2026-08-01T00:00:00Z',
  status: 'active',
  row_version: 1,
  created_at: '2026-08-01T00:00:00Z',
  updated_at: '2026-08-01T00:00:00Z',
  ...over,
});

const onCallNow: OnCallNowDTO = {
  schedule_id: 'SCH-PRIMARY',
  at: '2026-08-06T10:00:00Z',
  user_id: 'U-PRIYA',
  display_name: 'Priya Nair',
};

const participants: OnCallParticipantDTO[] = [
  {
    participant_id: 'PART-1',
    schedule_id: 'SCH-PRIMARY',
    user_id: 'U-PRIYA',
    position: 0,
    row_version: 1,
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-01T00:00:00Z',
  },
  {
    participant_id: 'PART-2',
    schedule_id: 'SCH-PRIMARY',
    user_id: 'U-SECOND',
    position: 1,
    row_version: 1,
    created_at: '2026-08-01T00:00:00Z',
    updated_at: '2026-08-01T00:00:00Z',
  },
];

interface Fixture {
  list?: unknown;
  onCall?: unknown;
  participants?: unknown;
}

function routedFetch(fx: Fixture = {}) {
  return vi.fn().mockImplementation(async (url: string) => {
    const u = String(url);
    const ok = (body: unknown) => ({ ok: true, status: 200, json: async () => body });

    if (u.includes('/auth/config')) return ok({ auth_enabled: false });

    // Order matters: "/on-call-schedules/{id}/on-call" and ".../participants"
    // both contain the substring "on-call-schedules", so the more specific
    // suffix checks must run before the generic list check.
    if (/\/on-call-schedules\/[^/?]+\/on-call(\?|$)/.test(u)) {
      if (fx.onCall === 'ERROR') return { ok: false, status: 500, json: async () => ({ title: 'boom', status: 500 }) };
      return ok(fx.onCall ?? onCallNow);
    }
    if (u.includes('/participants')) {
      if (fx.participants === 'ERROR') return { ok: false, status: 500, json: async () => ({ title: 'boom', status: 500 }) };
      return ok(fx.participants ?? { items: participants });
    }
    if (u.includes('/on-call-schedules')) {
      if (fx.list === 'ERROR') {
        return { ok: false, status: 500, json: async () => ({ title: 'internal error', status: 500, detail: 'boom' }) };
      }
      return ok(fx.list ?? { items: [] });
    }

    return ok({});
  });
}

beforeEach(() => window.history.replaceState(null, '', '/on-call'));
afterEach(() => vi.unstubAllGlobals());

describe('on-call board', () => {
  it('renders the current on-call and the ordered participant roster', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [schedule()] } }));
    renderApp();

    expect(await screen.findByRole('heading', { name: /^On-call/ })).toBeInTheDocument();
    expect(await screen.findByText('Primary on-call')).toBeInTheDocument();
    expect(await screen.findByText('Priya Nair')).toBeInTheDocument();
    expect(screen.getByText('12h')).toBeInTheDocument();
    expect(screen.getByText('U-PRIYA')).toBeInTheDocument();
    expect(screen.getByText('U-SECOND')).toBeInTheDocument();
    expect(screen.getByText('on call now')).toBeInTheDocument();
  });

  it('excludes archived schedules from the board', async () => {
    vi.stubGlobal(
      'fetch',
      routedFetch({ list: { items: [schedule(), schedule({ schedule_id: 'SCH-OLD', name: 'Retired rotation', status: 'archived' })] } }),
    );
    renderApp();

    await screen.findByText('Primary on-call');
    expect(screen.queryByText('Retired rotation')).not.toBeInTheDocument();
  });

  it('degrades on-call/participant fetch failure without blanking the card', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [schedule()] }, onCall: 'ERROR', participants: 'ERROR' }));
    renderApp();

    await screen.findByText('Primary on-call');
    await waitFor(() => expect(screen.getAllByText('Could not load').length).toBe(2));
  });

  it('shows an explicit empty state with no schedules', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [] } }));
    renderApp();

    expect(await screen.findByText('No on-call schedules have been created yet.')).toBeInTheDocument();
  });

  it('shows an explicit empty state when every schedule is archived', async () => {
    vi.stubGlobal('fetch', routedFetch({ list: { items: [schedule({ status: 'archived' })] } }));
    renderApp();

    expect(await screen.findByText('No active on-call schedules.')).toBeInTheDocument();
  });

  it('shows an error banner on failure and lets the operator retry', async () => {
    const fetchMock = routedFetch({ list: 'ERROR' });
    vi.stubGlobal('fetch', fetchMock);
    renderApp();

    expect(await screen.findByRole('alert')).toHaveTextContent('internal error');

    const callsBefore = fetchMock.mock.calls.length;
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));

    await vi.waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(callsBefore));
  });
});
