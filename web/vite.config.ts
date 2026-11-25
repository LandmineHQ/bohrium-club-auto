import fs from "node:fs"
import path from "node:path"
import { defineConfig } from "vite"
import react from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"

const GITKEEP_CONTENT =
  "# Placeholder so Go embed compiles before `npm run build --prefix web`\n"

export default defineConfig({
  plugins: [
    react(),
    tailwindcss(),
    {
      name: "preserve-dist-gitkeep",
      closeBundle() {
        const target = path.resolve(__dirname, "dist/.gitkeep")
        fs.mkdirSync(path.dirname(target), { recursive: true })
        fs.writeFileSync(target, GITKEEP_CONTENT)
      },
    },
  ],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
  },
  server: {
    port: 5174,
    proxy: {
      "/api": "http://127.0.0.1:8788",
      "/v1": "http://127.0.0.1:8788",
      "/healthz": "http://127.0.0.1:8788",
    },
  },
})
