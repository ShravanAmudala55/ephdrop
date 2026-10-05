"use strict";
// Builds the Go program (core/cmd/ephdropd) into desktop/bin for this computer,
// or for another one: node scripts/build-daemon.js windows amd64
const { spawnSync } = require("child_process");
const path = require("path");
const fs = require("fs");

const goos = process.argv[2] || process.platform.replace("win32", "windows");
const goarch = process.argv[3] || (process.arch === "x64" ? "amd64" : process.arch);
const exe = goos === "windows" ? "ephdropd.exe" : "ephdropd";
const out = path.join(__dirname, "..", "bin", exe);
fs.mkdirSync(path.dirname(out), { recursive: true });

const r = spawnSync("go", ["build", "-trimpath", "-ldflags", "-s -w", "-o", out, "./cmd/ephdropd"], {
  cwd: path.join(__dirname, "..", "..", "core"),
  stdio: "inherit",
  env: { ...process.env, GOOS: goos, GOARCH: goarch, CGO_ENABLED: "0" },
});
if (r.status !== 0) process.exit(r.status || 1);
console.log(`built ${out} (${goos}/${goarch})`);
