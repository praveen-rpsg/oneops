import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Built assets are embedded into the Go binary and served by the control plane
// itself. Same-origin by construction, so no CORS layer is required.
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: '../internal/httpapi/webdist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        // Route pages are lazy-loaded (App.tsx), but every route shares
        // Cloudscape's component library through the always-loaded Shell —
        // without this, Rollup hoists it into the main entry chunk, which
        // stays over the 500 kB warning even after route-splitting. Isolating
        // it lets it cache independently of app code across deploys too.
        manualChunks: {
          cloudscape: ['@cloudscape-design/components', '@cloudscape-design/global-styles'],
        },
      },
    },
  },
  server: { proxy: { '/v1': 'http://localhost:8080' } },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/test-setup.ts',
    // A test can chain several `findBy*`/`waitFor` calls (e.g. open a panel,
    // then wait on its own async-loaded content); each now gets up to the
    // raised 5000ms `asyncUtilTimeout` (test-setup.ts), so the overall
    // per-test budget needs enough headroom that a slow-but-correct render
    // under load doesn't hit vitest's own timeout first (default 5000ms).
    testTimeout: 20_000,
    // Cap worker concurrency rather than let it float up to the host's full
    // core count: this suite's own flake (ADR-HARD-001) was reproduced on a
    // machine already loaded by OTHER processes, where every vitest worker
    // competing for the remaining CPU made `collect`/render slower for all
    // of them at once. A lower, fixed ceiling trades a small amount of
    // best-case wall-clock time for the suite not amplifying contention it
    // didn't cause. 4 was chosen as comfortably under this dev machine's 10
    // cores, leaving room for whatever else is running alongside it.
    poolOptions: { threads: { minThreads: 1, maxThreads: 4 } },
  },
});
