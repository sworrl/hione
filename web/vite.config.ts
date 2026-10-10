import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// The build goes straight into the Go server's embed directory, so `go build` ships it.
export default defineConfig({
  plugins: [react()],
  base: './',
  build: { outDir: '../internal/server/ui', emptyOutDir: true, chunkSizeWarningLimit: 800 },
  server: { proxy: { '/api': { target: 'http://127.0.0.1:8484', ws: true } } },
});
