import { render } from '@testing-library/react';
import { BrowserRouter } from 'react-router-dom';
import App from './App';

/**
 * `main.tsx` wraps `App` in `BrowserRouter`; tests render `App` directly, so they
 * need the same wrapper. `BrowserRouter` (not `MemoryRouter`) is deliberate: tests
 * drive navigation via `window.history`, and the console's own URL-as-state code
 * (filters, deep links) reads `window.location` directly.
 */
export function renderApp() {
  return render(
    <BrowserRouter future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>
      <App />
    </BrowserRouter>,
  );
}
