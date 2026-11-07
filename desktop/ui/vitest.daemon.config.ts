// The run of the dashboard's tests against a real harmflowd: `make test-desktop-daemon` builds the daemon, names it in
// HARMFLOW_TEST_DAEMON, and runs `pnpm -C desktop test:daemon`. The interface's own config, its defines included, with
// a test section of its own: merged instead, its include would list every unit test as well.
import { defineConfig } from "vitest/config";
import base, { DAEMON_TESTS } from "./vite.config";

export default defineConfig({
  ...base,
  test: {
    environment: "node",
    include: DAEMON_TESTS,
    // The daemon's start and an execution's run, which the tests wait for as the integration suite does.
    testTimeout: 300_000,
    hookTimeout: 120_000,
    // The transport opens an EventSource, which a Node that keeps it experimental gives only behind this flag.
    execArgv: typeof EventSource === "undefined" ? ["--experimental-eventsource"] : [],
  },
});
