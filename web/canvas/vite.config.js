import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],

  // Kept in this one file on purpose: a second config file for the tests is a
  // second thing to keep in sync when the build changes.
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test-setup.js'],
    // Co-located with the code they guard. node_modules and dist are excluded
    // by default.
    include: ['src/**/*.test.{js,jsx}'],
  },
})