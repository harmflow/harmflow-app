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
// page and the daemon's CSRF check holds (D-090, FS-009 FR-005). Inside Tauri the Rust core does this instead.

interface Runtime {
  address: string;
  tokenStore?: "keychain" | "file";
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
  define: { __APP_VERSION__: JSON.stringify(pkg.version) },
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
  },
});
