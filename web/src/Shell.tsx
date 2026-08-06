import { Suspense, useCallback, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import { Outlet, useLocation, useNavigate } from 'react-router-dom';
import AppLayout from '@cloudscape-design/components/app-layout';
import BreadcrumbGroup from '@cloudscape-design/components/breadcrumb-group';
import Box from '@cloudscape-design/components/box';
import SideNavigation from '@cloudscape-design/components/side-navigation';
import type { SideNavigationProps } from '@cloudscape-design/components/side-navigation';
import Spinner from '@cloudscape-design/components/spinner';
import SplitPanel from '@cloudscape-design/components/split-panel';
import TopNavigation from '@cloudscape-design/components/top-navigation';
import { getSubject, signOut } from './auth';
import { currentMode, toggleMode } from './theme';
import { Mode } from '@cloudscape-design/global-styles';

/** Matches App.tsx's "Signing in…" idiom; shown while a route's lazy chunk loads. */
const ROUTE_LOADING_FALLBACK = (
  <Box margin="xxl" textAlign="center" padding="xxl">
    <div role="status" aria-busy="true">
      <Spinner size="large" /> Loading…
    </div>
  </Box>
);

/**
 * The seam a routed page uses to drive the shell's shared `SplitPanel` (E7-UI.2's
 * incident drill-down; any future screen can reuse it). `useOutletContext` is how
 * a page reaches it — Shell owns the single `AppLayout` instance every route
 * renders inside, so the split panel cannot be a per-page component.
 */
export interface ShellSplitPanelContext {
  openSplitPanel: (header: string, content: ReactNode) => void;
  closeSplitPanel: () => void;
  /**
   * Whether the panel is currently visible. Additive to the original
   * open/close pair (ADR-HARD-002): a page whose split-panel content depends
   * on data that keeps resolving after the panel opened (TopologyPage's
   * incident/health overlay) needs this to decide whether to *refresh* an
   * already-open panel without ever reopening one the user closed. Existing
   * consumers that only destructure `openSplitPanel`/`closeSplitPanel` are
   * unaffected.
   */
  isSplitPanelOpen: boolean;
}

const NAV_ITEMS: SideNavigationProps.Item[] = [
  { type: 'link', text: 'Estate / Governance', href: '/' },
  { type: 'link', text: 'NOC / Overview', href: '/noc' },
  { type: 'link', text: 'Incidents', href: '/incidents' },
  { type: 'link', text: 'Alerts', href: '/alerts' },
  { type: 'link', text: 'Maintenance', href: '/maintenance' },
  { type: 'link', text: 'On-call', href: '/on-call' },
  { type: 'link', text: 'Escalation', href: '/escalation' },
  { type: 'link', text: 'Topology', href: '/topology' },
  { type: 'link', text: 'Dashboards', href: '/dashboards' },
];

/** The path a side-nav item is considered active for — longest-prefix match, `/` is exact. */
function activeHrefFor(pathname: string): string {
  if (pathname === '/' || pathname.startsWith('/artifacts')) return '/';
  if (pathname.startsWith('/noc')) return '/noc';
  if (pathname.startsWith('/incidents')) return '/incidents';
  if (pathname.startsWith('/alerts')) return '/alerts';
  if (pathname.startsWith('/maintenance')) return '/maintenance';
  if (pathname.startsWith('/on-call')) return '/on-call';
  if (pathname.startsWith('/escalation')) return '/escalation';
  if (pathname.startsWith('/topology')) return '/topology';
  if (pathname.startsWith('/dashboards')) return '/dashboards';
  return '/';
}

const CRUMB_LABEL: Record<string, string> = {
  '': 'Estate',
  artifacts: 'Estate',
  noc: 'NOC / Overview',
  incidents: 'Incidents',
  alerts: 'Alerts',
  maintenance: 'Maintenance',
  'on-call': 'On-call',
  escalation: 'Escalation',
  topology: 'Topology',
  dashboards: 'Dashboards',
};

function useBreadcrumbs() {
  const location = useLocation();
  return useMemo(() => {
    const segments = location.pathname.split('/').filter(Boolean);
    const items = [{ text: 'OneOps', href: '/' }];
    if (segments.length === 0) {
      items.push({ text: 'Estate', href: '/' });
      return items;
    }
    const [first, ...rest] = segments;
    items.push({ text: CRUMB_LABEL[first] ?? first, href: `/${first}` });
    if (first === 'artifacts' && rest[0]) {
      items.push({ text: rest[0], href: location.pathname });
    }
    return items;
  }, [location.pathname]);
}

/**
 * The reusable home for every section of the console: top navigation (identity,
 * session, theme), side navigation (Estate/Governance, NOC/Overview, Incidents,
 * Alerts, Maintenance, On-call, Escalation, Topology and Dashboards are all live) and breadcrumbs. Routed content renders in
 * the `content` slot via `<Outlet/>`; the incident board (E7-UI.2) and the
 * alerts board (E7.3c) drive the shared `SplitPanel` through
 * `ShellSplitPanelContext`.
 */
export function Shell() {
  const navigate = useNavigate();
  const location = useLocation();
  const breadcrumbs = useBreadcrumbs();
  const [mode, setMode] = useState<Mode>(currentMode());
  const subject = getSubject();

  const [splitPanel, setSplitPanel] = useState<{ header: string; content: ReactNode } | null>(null);
  const [splitPanelOpen, setSplitPanelOpen] = useState(false);

  const openSplitPanel = useCallback((header: string, content: ReactNode) => {
    setSplitPanel({ header, content });
    setSplitPanelOpen(true);
  }, []);
  const closeSplitPanel = useCallback(() => setSplitPanelOpen(false), []);
  const splitPanelContext = useMemo<ShellSplitPanelContext>(
    () => ({ openSplitPanel, closeSplitPanel, isSplitPanelOpen: splitPanelOpen }),
    [openSplitPanel, closeSplitPanel, splitPanelOpen],
  );

  // A split panel belongs to the page that opened it — leaving that route
  // (not merely closing the panel) clears its content so the next page never
  // inherits a stale drill-down.
  useEffect(() => {
    setSplitPanel(null);
    setSplitPanelOpen(false);
  }, [location.pathname]);

  return (
    <>
      <div id="top-nav">
        <TopNavigation
          identity={{ href: '/', title: 'OneOps', onFollow: (e) => { e.preventDefault(); navigate('/'); } }}
          utilities={[
            {
              type: 'button',
              text: mode === Mode.Dark ? 'Light mode' : 'Dark mode',
              iconName: 'light-dark',
              onClick: () => setMode(toggleMode()),
            },
            ...(subject
              ? ([
                  { type: 'button', text: subject, iconName: 'user-profile', disableTextCollapse: true },
                  { type: 'button', text: 'Sign out', iconName: 'sign-out', onClick: () => signOut() },
                ] as const)
              : []),
          ]}
        />
      </div>
      <AppLayout
        headerSelector="#top-nav"
        toolsHide
        navigation={
          <SideNavigation
            header={{ text: 'OneOps', href: '/' }}
            activeHref={activeHrefFor(location.pathname)}
            items={NAV_ITEMS}
            onFollow={(e) => {
              e.preventDefault();
              if (!e.detail.external) navigate(e.detail.href);
            }}
          />
        }
        breadcrumbs={<BreadcrumbGroup items={breadcrumbs} onFollow={(e) => { e.preventDefault(); navigate(e.detail.href); }} />}
        content={
          <Suspense fallback={ROUTE_LOADING_FALLBACK}>
            <Outlet context={splitPanelContext} />
          </Suspense>
        }
        splitPanelOpen={splitPanelOpen}
        onSplitPanelToggle={({ detail }) => setSplitPanelOpen(detail.open)}
        splitPanel={splitPanel ? <SplitPanel header={splitPanel.header}>{splitPanel.content}</SplitPanel> : undefined}
      />
    </>
  );
}
