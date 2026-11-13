import {defineConfig} from '@playwright/test';
export default defineConfig({testDir: 'test/browser', workers: 1, timeout: 45_000,
  reporter: 'line', use: {headless: true, ignoreHTTPSErrors: true}, outputDir: 'test-results'});
