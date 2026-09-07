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
    },
  },
});
