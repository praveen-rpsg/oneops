import { useEffect, useState } from 'react';
import { Mode } from '@cloudscape-design/global-styles';

// The hand-rolled SVG's color palette (E7.3b-2, ADR-NOC-007). Raw SVG
// elements cannot consume Cloudscape component CSS directly: every color
// custom property `@cloudscape-design/components` emits is scoped per
// component with a hash suffix generated at build time (e.g.
// `--color-text-status-error-<hash>`), and there is no separately installed
// `@cloudscape-design/design-tokens` package in this project to give them
// stable, importable names. Rather than add a new dependency for one screen,
// this fixed palette mirrors Cloudscape's own status semantics — the same
// error/warning/success/info split ADR-NOC-002/003 already carry forward for
// every `StatusIndicator` in this console (red=error, amber=warning,
// blue/grey=neutral) — and switches with the app's own dark/light mode
// (theme.ts) so the SVG still themes correctly.

export interface TopologyPalette {
  background: string;
  nodeFill: string;
  nodeStroke: string;
  nodeText: string;
  nodeSecondaryText: string;
  edgeStroke: string;
  backEdgeStroke: string;
  errorFill: string;
  errorStroke: string;
  warningFill: string;
  warningStroke: string;
  selectedStroke: string;
}

const LIGHT: TopologyPalette = {
  background: '#ffffff',
  nodeFill: '#f2f3f3',
  nodeStroke: '#7d8998',
  nodeText: '#16191f',
  nodeSecondaryText: '#5f6b7a',
  edgeStroke: '#7d8998',
  backEdgeStroke: '#8d6605',
  errorFill: '#fdf3f4',
  errorStroke: '#d91515',
  warningFill: '#fef9f0',
  warningStroke: '#8d6605',
  selectedStroke: '#0972d3',
};

const DARK: TopologyPalette = {
  background: '#0f1b2a',
  nodeFill: '#232f3e',
  nodeStroke: '#8d99a8',
  nodeText: '#e9ebed',
  nodeSecondaryText: '#b6bec9',
  edgeStroke: '#8d99a8',
  backEdgeStroke: '#e0a712',
  errorFill: '#3a1414',
  errorStroke: '#ff5d5d',
  warningFill: '#3a2e0d',
  warningStroke: '#e0a712',
  selectedStroke: '#539fe5',
};

export function topologyPalette(mode: Mode): TopologyPalette {
  return mode === Mode.Dark ? DARK : LIGHT;
}

/**
 * Tracks the effective mode live. `theme.ts`'s `applyMode` toggles an
 * `awsui-dark-mode` class on `document.body`; every other screen in this
 * console re-themes for free because Cloudscape's own CSS reacts to that
 * class, but hand-rolled SVG colors are plain attribute values computed in
 * JS, so this page needs to notice the class changing (the dark/light
 * toggle in `Shell`) rather than reading the mode once at mount.
 */
export function useTopologyMode(): Mode {
  const [mode, setMode] = useState<Mode>(() =>
    document.body.classList.contains('awsui-dark-mode') ? Mode.Dark : Mode.Light,
  );

  useEffect(() => {
    const target = document.body;
    const sync = () => setMode(target.classList.contains('awsui-dark-mode') ? Mode.Dark : Mode.Light);
    sync();
    const observer = new MutationObserver(sync);
    observer.observe(target, { attributes: true, attributeFilter: ['class'] });
    return () => observer.disconnect();
  }, []);

  return mode;
}
