import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

const backend = process.env.AWG_UI_BACKEND || "http://127.0.0.1:51821";
// Keep Host aligned with the browser Origin for the backend's CSRF check.
const proxy = { target: backend, changeOrigin: false };

export default defineConfig({
  root: "web",
  base: "/static/",
  plugins: [react()],
  build: {
    outDir: "../internal/server/static",
    emptyOutDir: true,
    assetsDir: "assets",
    sourcemap: false,
  },
  server: {
    proxy: {
      "/api": proxy,
      "/clients": proxy,
    },
  },
});
