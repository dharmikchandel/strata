import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// The built page is embedded in the stratactl binary (go:embed ui), so it is
// written straight into cmd/stratactl/ui and committed; `make web-check` fails
// when that copy is out of date with this source.
//
// `npm run dev` serves the page with live reload and forwards /api to a running
// `stratactl ui` (STRATA_UI, default 127.0.0.1:7080).
export default defineConfig({
  plugins: [react()],
  build: {
    outDir: process.env.STRATA_WEB_OUT ?? '../cmd/stratactl/ui',
    emptyOutDir: true,
    // The page is served under a CSP without 'unsafe-inline': keep every
    // script and style in files, and skip the preload polyfill (modern browsers only).
    modulePreload: { polyfill: false },
    assetsInlineLimit: 0,
  },
  server: {
    proxy: { '/api': `http://${process.env.STRATA_UI ?? '127.0.0.1:7080'}` },
  },
});
