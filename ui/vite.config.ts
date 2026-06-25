import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// In dev, proxy the agent API (and its SSE stream) to the local agent on :8081.
// Override the target with AURORA_API_TARGET when the agent runs elsewhere.
const apiTarget = process.env.AURORA_API_TARGET ?? "http://localhost:8081";

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      "/api": {
        target: apiTarget,
        changeOrigin: true,
      },
    },
  },
});
