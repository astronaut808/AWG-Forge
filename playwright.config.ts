import { defineConfig } from "@playwright/test";

const port = Number(process.env.UI_TEST_PORT || 51924);
if (!Number.isInteger(port) || port < 1024 || port > 65532) {
  throw new Error("UI_TEST_PORT must be an integer between 1024 and 65532");
}

const projects = [
  { name: "desktop-light", use: { viewport: { width: 1440, height: 1000 }, colorScheme: "light" as const, locale: "en-US" } },
  { name: "desktop-dark", use: { viewport: { width: 1440, height: 1000 }, colorScheme: "dark" as const, locale: "en-US" } },
  { name: "mobile-light-ru", use: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, colorScheme: "light" as const, locale: "ru-RU" } },
  { name: "mobile-dark-ru", use: { viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, colorScheme: "dark" as const, locale: "ru-RU" } },
].map((project, index) => ({ ...project, use: { ...project.use, baseURL: `http://127.0.0.1:${port + index}` } }));

export default defineConfig({
  testDir: "./web/tests",
  outputDir: process.env.UI_TEST_OUTPUT_DIR || "test-results",
  fullyParallel: false,
  workers: 4,
  forbidOnly: Boolean(process.env.CI),
  retries: 0,
  timeout: 30_000,
  reporter: "list",
  use: {
    browserName: "chromium",
    // Exports contain disposable test keys; never upload them as CI artifacts.
    trace: "off",
    screenshot: "off",
    video: "off",
  },
  projects,
  // Isolate state and authentication limits for each browser configuration.
  webServer: projects.map((project, index) => ({
    command: "node web/tests/server.mjs",
    env: { WEBUI_PORT: String(port + index) },
    url: project.use.baseURL,
    reuseExistingServer: false,
    timeout: 120_000,
    gracefulShutdown: { signal: "SIGTERM" as const, timeout: 10_000 },
  })),
});
