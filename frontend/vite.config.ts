import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";
import path from "node:path";

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  server: {
    port: 5173,
    proxy: {
      // Proxy /api to the Go backend during dev so we don't need CORS.
      "/api": {
        target: "http://localhost:8080",
        changeOrigin: false,
      },
      // Proxy the ingress path tree too, so the same-origin URL built
      // by buildIngressURL reaches the Go reverse proxy instead of
      // Vite's SPA fallback (which would otherwise serve index.html,
      // mount React Router, and the catch-all <Route path="*"> would
      // bounce the user to /dashboard).
      //
      // The key starts with "^" so Vite treats it as a regex (see
      // doesProxyContextMatchUrl in vite/dist). `-` is not a regex
      // metacharacter outside character classes, so we don't need to
      // escape it.
      "^/-/apps(/.*)?$": {
        target: "http://localhost:8080",
        changeOrigin: false,
      },
    },
  },
});
