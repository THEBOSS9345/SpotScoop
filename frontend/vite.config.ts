import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import path from 'path'

export default defineConfig({
  plugins: [react()],
  base: '',
  build: {
    outDir: path.resolve(__dirname, '../src/app/ui/dist'),
    // outDir sits outside the Vite project root, so it is not cleared by
    // default and hashed bundles from previous builds pile up in the embedded
    // output. Clear it so dist only ever holds the current build.
    emptyOutDir: true,
  },
})
