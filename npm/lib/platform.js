"use strict";

const os = require("os");

const SUPPORTED_PLATFORMS = new Set(["darwin", "linux", "win32"]);
const SUPPORTED_ARCHITECTURES = new Set(["x64", "arm64"]);

function getBinaryName(platform = os.platform(), architecture = os.arch()) {
  if (!SUPPORTED_PLATFORMS.has(platform) || !SUPPORTED_ARCHITECTURES.has(architecture)) {
    throw new Error(`unsupported platform ${platform}-${architecture}`);
  }

  const extension = platform === "win32" ? ".exe" : "";
  return `memor-${platform}-${architecture}${extension}`;
}

module.exports = { getBinaryName };