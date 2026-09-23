import path from 'node:path';
import tailwindcss from '@tailwindcss/vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vitest/config';

/**
 * Web UI（FR-14.4：Svelte + TypeScript + Vite + shadcn-svelte）。
 *
 * dev/preview 经 proxy 把 /api 与探针转发到本地控制面（默认 127.0.0.1:7788），
 * 使浏览器侧始终同源：不必给控制面加 CORS，也不必把 admin token 放进 URL。
 * 控制面地址可用 FLEET_API 覆盖（本地起控制面联调时用）。
 */
const target = process.env.FLEET_API ?? 'http://127.0.0.1:7788';
const proxy = {
  '/api': { target, changeOrigin: true },
  '/healthz': { target, changeOrigin: true },
  '/readyz': { target, changeOrigin: true },
};

export default defineConfig({
  plugins: [tailwindcss(), svelte()],
  resolve: {
    alias: {
      $lib: path.resolve('./src/lib'),
    },
  },
  server: { proxy },
  preview: { proxy },
  test: {
    environment: 'node',
    include: ['tests/**/*.test.ts'],
  },
});
