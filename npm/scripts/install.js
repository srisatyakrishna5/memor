"use strict";

const https = require("https");
const fs = require("fs");
const path = require("path");
const os = require("os");
const { getBinaryName } = require("../lib/platform");

const REPO = "akashchekka/memor";

function getVersion() {
  const pkg = JSON.parse(
    fs.readFileSync(path.join(__dirname, "..", "package.json"), "utf8")
  );
  return pkg.version;
}

function fetch(url) {
  return new Promise((resolve, reject) => {
    https
      .get(url, { headers: { "User-Agent": "memor-npm-installer" } }, (res) => {
        // Follow redirects (GitHub releases redirect to S3)
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          return fetch(res.headers.location).then(resolve, reject);
        }

        if (res.statusCode !== 200) {
          reject(new Error(`Download failed: HTTP ${res.statusCode} from ${url}`));
          return;
        }

        const chunks = [];
        res.on("data", (chunk) => chunks.push(chunk));
        res.on("end", () => resolve(Buffer.concat(chunks)));
        res.on("error", reject);
      })
      .on("error", reject);
  });
}

async function install() {
  try {
    const binaryName = getBinaryName();
    const version = getVersion();
    const url = `https://github.com/${REPO}/releases/download/v${version}/${binaryName}`;
    const dest = path.join(__dirname, "..", "bin", binaryName);

    console.log(`memor: downloading ${binaryName} v${version}...`);
    const data = await fetch(url);
    fs.writeFileSync(dest, data);

    // Make executable on Unix
    if (os.platform() !== "win32") {
      fs.chmodSync(dest, 0o755);
    }

    console.log(`memor: installed to ${dest}`);
  } catch (err) {
    console.error(`memor: ${err.message}`);
    console.error(`memor: you can install manually from https://github.com/${REPO}/releases`);
    process.exit(1);
  }
}

install();
