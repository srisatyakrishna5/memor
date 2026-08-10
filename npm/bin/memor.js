#!/usr/bin/env node

"use strict";

const { execFileSync } = require("child_process");
const path = require("path");
const { getBinaryName } = require("../lib/platform");

try {
  const binaryPath = path.join(__dirname, getBinaryName());
  execFileSync(binaryPath, process.argv.slice(2), { stdio: "inherit" });
} catch (err) {
  if (Number.isInteger(err.status)) {
    process.exit(err.status);
  }
  console.error(`memor: ${err.message}`);
  process.exit(1);
}
