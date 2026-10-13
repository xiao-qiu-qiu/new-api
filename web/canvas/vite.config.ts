import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import react from '@vitejs/plugin-react';
import { defineConfig } from 'vite';
const root = dirname(fileURLToPath(import.meta.url));
export default defineConfig({
    base: '/canvas-app/',
    plugins: [react()],
    resolve: { alias: { '@': resolve(root, 'src') } },
    define: { __APP_VERSION__: JSON.stringify('new-api'), __APP_RELEASES__: '[]' },
    build: { outDir: '../public/canvas-app', emptyOutDir: true },
});
