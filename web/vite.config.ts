import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Built assets are embedded into the Go binary and served by the control plane
// itself. Same-origin by construction, so no CORS layer is required.
export default defineConfig({
  plugins: [react()],
  build: { outDir: '../internal/httpapi/webdist', emptyOutDir: true },
  server: { proxy: { '/v1': 'http://localhost:8080' } },
  test: { environment: 'jsdom', globals: true, setupFiles: './src/test-setup.ts' },
});
