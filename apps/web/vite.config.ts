import { configDefaults, defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    strictPort: true,
    host: "0.0.0.0",
    proxy: {
      "/api/health": {
        target: "http://api:8080",
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/api/, ""),
      },
      "/api/v1": {
        target: "http://api:8080",
        changeOrigin: true,
      },
    },
  },
  build: {
    sourcemap: true,
    target: "es2024",
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    clearMocks: true,
    exclude: [...configDefaults.exclude, "e2e/**"],
  },
});
