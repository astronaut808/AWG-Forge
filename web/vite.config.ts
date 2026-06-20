import { defineConfig } from "vite";
import preact from "@preact/preset-vite";

export default defineConfig({
  root: "web",
  base: "/static/",
  plugins: [preact()],
  build: {
    outDir: "../internal/server/static",
    emptyOutDir: true,
    assetsDir: "assets",
    sourcemap: false,
  },
  server: {
    proxy: {
      "/api": "http://127.0.0.1:51821",
      "/clients": "http://127.0.0.1:51821",
    },
  },
});
