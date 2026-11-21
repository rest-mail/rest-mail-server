import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import wails from "@wailsio/runtime/plugins/vite";
import tailwindcss from '@tailwindcss/vite';
import path from 'node:path';
import { devMock } from './dev/mock';

// https://vitejs.dev/config/
export default defineConfig(({ command }) => ({
  define: { 'import.meta.env.VITE_DESKTOP': '"true"', 'import.meta.env.DESKTOP_MOCK': JSON.stringify(command === 'serve' && process.env.DESKTOP_MOCK === '1') },
  resolve: {
    alias: { '@': path.resolve(__dirname, '../../webmail/src'), '@webmail': path.resolve(__dirname, '../../webmail/src') },
    dedupe: ['react', 'react-dom'],
  },
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
    fs: { allow: [path.resolve(__dirname, '../..')] },
  },
  plugins: [react(), tailwindcss(), wails("./bindings"), ...(command === 'serve' && process.env.DESKTOP_MOCK === '1' ? [devMock()] : [])],
}));
