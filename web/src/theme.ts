import { applyMode, Mode } from '@cloudscape-design/global-styles';

// Dark mode follows the OS by default. An explicit user choice overrides that
// for the tab's lifetime only — sessionStorage, not localStorage, matching the
// no-persistent-client-state posture used for the auth token (auth.ts).
const KEY = 'oneops.theme-mode';

function systemPrefersDark(): boolean {
  return (
    typeof window.matchMedia === 'function' &&
    window.matchMedia('(prefers-color-scheme: dark)').matches
  );
}

function storedMode(): Mode | null {
  const v = sessionStorage.getItem(KEY);
  return v === Mode.Dark || v === Mode.Light ? v : null;
}

export function currentMode(): Mode {
  return storedMode() ?? (systemPrefersDark() ? Mode.Dark : Mode.Light);
}

/** Applies the effective mode and keeps it in sync with OS changes until the user overrides it. */
export function initTheme(): void {
  applyMode(currentMode());
  if (typeof window.matchMedia !== 'function') return;
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    if (!storedMode()) applyMode(currentMode());
  });
}

/** Toggles and persists the override for this tab; returns the mode now in effect. */
export function toggleMode(): Mode {
  const next = currentMode() === Mode.Dark ? Mode.Light : Mode.Dark;
  sessionStorage.setItem(KEY, next);
  applyMode(next);
  return next;
}
