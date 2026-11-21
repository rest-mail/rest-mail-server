import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import path from 'node:path';

export default defineConfig({
  plugins: [react()],
  define: { 'import.meta.env.VITE_DESKTOP': '"true"' },
  resolve: { alias: { '@': path.resolve(__dirname, '../../webmail/src'), '@webmail': path.resolve(__dirname, '../../webmail/src') }, dedupe: ['react', 'react-dom'] },
  test: { environment: 'jsdom', setupFiles: ['./src/test-setup.ts'], include: ['src/**/*.test.{ts,tsx}'], server: { deps: { inline: [/webmail/] } } },
});
