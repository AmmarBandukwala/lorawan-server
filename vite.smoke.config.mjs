import { defineConfig } from 'vite'

// Throwaway config used only to render App in Node for a smoke check.
export default defineConfig({
  build: {
    ssr: 'smoke/entry.tsx',
    outDir: 'smoke/dist',
    minify: false,
    emptyOutDir: true,
  },
})
