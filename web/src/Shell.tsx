import { useMemo, useState } from 'react';
import { Outlet, useLocation, useNavigate } from 'react-router-dom';
import AppLayout from '@cloudscape-design/components/app-layout';
import BreadcrumbGroup from '@cloudscape-design/components/breadcrumb-group';
import SideNavigation from '@cloudscape-design/components/side-navigation';
import type { SideNavigationProps } from '@cloudscape-design/components/side-navigation';
import TopNavigation from '@cloudscape-design/components/top-navigation';
import { getSubject, signOut } from './auth';
import { currentMode, toggleMode } from './theme';
import { Mode } from '@cloudscape-design/global-styles';

const NAV_ITEMS: SideNavigationProps.Item[] = [
  { type: 'link', text: 'Estate / Governance', href: '/' },
  { type: 'link', text: 'NOC / Overview', href: '/noc' },
  { type: 'link', text: 'Incidents', href: '/incidents' },
];

/** The path a side-nav item is considered active for — longest-prefix match, `/` is exact. */
function activeHrefFor(pathname: string): string {
  if (pathname === '/' || pathname.startsWith('/artifacts')) return '/';
  if (pathname.startsWith('/noc')) return '/noc';
  if (pathname.startsWith('/incidents')) return '/incidents';
  return '/';
}

const CRUMB_LABEL: Record<string, string> = {
  '': 'Estate',
  artifacts: 'Estate',
  noc: 'NOC / Overview',
  incidents: 'Incidents',
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
 * session, theme), side navigation (Estate/Governance today; NOC and Incidents are
 * placeholders until E7-UI.1/E7-UI.2 land) and breadcrumbs. Routed content renders
 * in the `content` slot via `<Outlet/>`.
 */
export function Shell() {
  const navigate = useNavigate();
  const location = useLocation();
  const breadcrumbs = useBreadcrumbs();
  const [mode, setMode] = useState<Mode>(currentMode());
  const subject = getSubject();

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
        content={<Outlet />}
      />
    </>
  );
}
