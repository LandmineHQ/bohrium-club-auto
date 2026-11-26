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
    rollupOptions: {
      output: {
        manualChunks(id) {
          const norm = id.replace(/\\/g, "/")
          if (!norm.includes("/node_modules/")) return
          if (
            norm.includes("/node_modules/d3-") ||
            norm.includes("/node_modules/victory-vendor/") ||
            norm.includes("/node_modules/internmap/")
          ) {
            return "d3-vendor"
          }
          if (
            norm.includes("/node_modules/recharts/es6/cartesian/CartesianGrid.js") ||
            norm.includes("/node_modules/recharts/es6/cartesian/XAxis.js") ||
            norm.includes("/node_modules/recharts/es6/cartesian/YAxis.js") ||
            norm.includes("/node_modules/recharts/es6/component/Tooltip.js") ||
            norm.includes("/node_modules/recharts/es6/component/Legend.js") ||
            norm.endsWith("/node_modules/recharts/es6/index.js")
          ) {
            return "charts-views"
          }
          if (norm.includes("/node_modules/recharts/")) {
            return "charts"
          }
          if (
            norm.includes("/node_modules/@radix-ui/") ||
            norm.includes("/node_modules/lucide-react/") ||
            norm.includes("/node_modules/sonner/")
          ) {
            return "ui-vendor"
          }
          if (
            norm.includes("/node_modules/react/") ||
            norm.includes("/node_modules/react-dom/") ||
            norm.includes("/node_modules/scheduler/") ||
            norm.includes("/node_modules/use-sync-external-store/") ||
            norm.includes("/node_modules/@reduxjs/") ||
            norm.includes("/node_modules/redux/") ||
            norm.includes("/node_modules/react-redux/") ||
            norm.includes("/node_modules/reselect/") ||
            norm.includes("/node_modules/immer/") ||
            norm.includes("/node_modules/es-toolkit/") ||
            norm.includes("/node_modules/clsx/")
          ) {
            return "react-core"
          }
        },
      },
    },
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
