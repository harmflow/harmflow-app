/// <reference types="vitest/config" />
import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import http from "node:http";
import { homedir } from "node:os";
import { join } from "node:path";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vite";
import pkg from "./package.json" with { type: "json" };

// In a plain browser the dashboard reaches the daemon through this dev server: /api is forwarded to the address in
// the daemon's runtime file, with the token added here and the Origin header dropped, so the token never reaches the
// page and the daemon's CSRF check holds. Inside Tauri the Rust core does this instead.

interface Runtime {
  address: string;
  tokenStore?: "keychain" | "file";
}

// The commit the interface is built from, as the Makefile reads it for the daemon: its first 7 characters, "-dirty"
// with uncommitted changes, and empty outside a checkout. `tauri build` builds the interface first, from the same tree.
function buildCommit(): string {
  const git = (...args: string[]) => execFileSync("git", args, { encoding: "utf8", stdio: ["ignore", "pipe", "ignore"] }).trim();
  try {
    const commit = git("rev-parse", "--verify", "--quiet", "HEAD").slice(0, 7);
    return git("status", "--porcelain") ? `${commit}-dirty` : commit;
  } catch {
    return "";
  }
}

function harmflowHome(): string {
  return process.env.HARMFLOW_HOME || join(homedir(), "Library", "Application Support", "HarmFlow");
}

let cached: { runtime: Runtime; token: string; at: number } | null = null;

function connection(): { runtime: Runtime; token: string } | null {
  if (cached && Date.now() - cached.at < 5000) return cached;
  try {
    const home = harmflowHome();
    const runtime = JSON.parse(readFileSync(join(home, "run", "daemon.json"), "utf8")) as Runtime;
    const token =
      runtime.tokenStore === "file"
        ? readFileSync(join(home, "run", "token"), "utf8").trim()
        : execFileSync("/usr/bin/security", ["find-generic-password", "-s", "com.harmflow.local-api", "-a", "token", "-w"], {
            encoding: "utf8",
          }).trim();
    cached = { runtime, token, at: Date.now() };
    return cached;
  } catch {
    cached = null;
    return null;
  }
}

function daemonProxy(): Plugin {
  return {
    name: "harmflow-daemon-proxy",
    configureServer(server) {
      server.middlewares.use("/api", (req, res) => {
        const conn = connection();
        if (!conn) {
          res.statusCode = 502;
          res.setHeader("Content-Type", "application/json");
          res.end(JSON.stringify({ error: { code: "internal", message: "The HarmFlow daemon is not running." } }));
          return;
        }
        const [host, port] = conn.runtime.address.split(":");
        const headers: http.OutgoingHttpHeaders = { ...req.headers, host: conn.runtime.address, authorization: `Bearer ${conn.token}` };
        delete headers.origin;
        delete headers.referer;
        delete headers.cookie;
        const upstream = http.request(
          { host, port: Number(port), method: req.method, path: `/api${req.url ?? ""}`, headers },
          (up) => {
            if (up.statusCode === 401) cached = null; // the token may have been replaced
            res.writeHead(up.statusCode ?? 502, up.headers);
            up.pipe(res);
          },
        );
        upstream.on("error", () => {
          cached = null;
          if (!res.headersSent) {
            res.statusCode = 502;
            res.setHeader("Content-Type", "application/json");
          }
          res.end(JSON.stringify({ error: { code: "internal", message: "The HarmFlow daemon did not answer." } }));
        });
        // The request's own "close" fires once its body is read; only a client that leaves the response aborts it.
        res.on("close", () => {
          if (!res.writableFinished) upstream.destroy();
        });
        req.pipe(upstream);
      });
    },
  };
}

export default defineConfig({
  plugins: [react(), daemonProxy()],
  // The build time is when Vite loads this config: under `vite dev` and `tauri dev`, when the dev server started.
  define: {
    __APP_VERSION__: JSON.stringify(pkg.version),
    __APP_COMMIT__: JSON.stringify(buildCommit()),
    __APP_BUILT_AT__: JSON.stringify(new Date().toISOString()),
  },
  clearScreen: false,
  server: {
    port: 1420,
    strictPort: true,
    // The contracts live at the repository root and are imported as JSON.
    fs: { allow: ["..", "../../contracts"] },
  },
  envPrefix: ["VITE_", "TAURI_ENV_"],
  build: {
    target: "safari16",
    sourcemap: false,
    chunkSizeWarningLimit: 1500,
  },
  test: {
    environment: "node",
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
    // Read and held only with --coverage (`pnpm test:coverage`). The include is explicit so that a source file
    // no test imports still counts; code nobody writes by hand does not.
    coverage: {
      provider: "v8",
      reporter: ["text", "html", "json-summary"],
      include: ["src/**/*.{ts,tsx}"],
      exclude: ["src/**/*.test.{ts,tsx}", "src/api/generated.ts", "src/main.tsx"],
      // The floors: the reading in docs/coverage.md, truncated to two decimals. Never autoUpdate: a floor moves by hand,
      // with its row in docs/coverage.md, in one commit.
      thresholds: {
        statements: 65.33,
        branches: 61.34,
        functions: 59.31,
        lines: 67.65,
      },
    },
  },
});
