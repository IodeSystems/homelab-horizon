import { defineConfig } from "vite";
import react from "@vitejs/plugin-react-swc";
import { TanStackRouterVite } from "@tanstack/router-plugin/vite";
import path from "node:path";

export default defineConfig({
  base: "/app/",
  plugins: [TanStackRouterVite({ routesDirectory: "src/routes" }), react()],
  resolve: {
    alias: {
      // WEB-3: tsconfig.app.json declares this path, so without it here the
      // type checker resolves `@/…` and the bundler does not — an import that
      // passes tsc and fails at build time.
      "@": path.resolve(__dirname, "src"),
    },
  },
  server: {
    // WEB-9. host: the dev server binds loopback rather than every interface,
    // so `npm run dev` on a shared box is not an open proxy to hz's admin API.
    // strictPort: fail instead of silently moving to 5174, which leaves the
    // proxy pointing at a port nothing is serving.
    host: "127.0.0.1",
    port: 5173,
    strictPort: true,
    proxy: {
      // ws: hz streams sync progress over a websocket; without this the dev
      // server proxies the handshake as a plain request and the stream dies.
      "/api": { target: "http://localhost:8080", ws: true },
      "/auth": { target: "http://localhost:8080", ws: true },
      "/health": { target: "http://localhost:8080", ws: true },
    },
  },
  build: {
    outDir: "dist",
  },
});
