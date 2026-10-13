'use strict';

// postinstall：从 GitHub Releases 下载与本机平台匹配的预编译二进制到 vendor/。
//
// 可通过以下方式跳过下载（例如离线环境或镜像策略）：
//   WXAPKG_KILLER_SKIP_DOWNLOAD=1 npm i wxapkg-killer-mcp
// 指定版本：
//   WXAPKG_KILLER_VERSION=v0.1.0 npm i wxapkg-killer-mcp
// 指定仓库（fork 场景）：
//   WXAPKG_REPO=EchoTide/wxapkg-killer npm i wxapkg-killer-mcp

const crypto = require('crypto');
const { execFileSync } = require('child_process');
const fs = require('fs');
const os = require('os');
const path = require('path');
const https = require('https');
const zlib = require('zlib');

const REPO = process.env.WXAPKG_REPO || 'EchoTide/wxapkg-killer';
const VERSION = process.env.WXAPKG_KILLER_VERSION || 'latest';

function platformInfo() {
  const platform = process.platform;
  const arch = process.arch === 'arm64' ? 'arm64' : process.arch === 'x64' ? 'amd64' : null;
  if (!arch) return null;
  if (platform === 'win32') return { os: 'windows', arch, ext: '.zip' };
  if (platform === 'darwin') return { os: 'darwin', arch, ext: '.tar.gz' };
  if (platform === 'linux') return { os: 'linux', arch, ext: '.tar.gz' };
  return null;
}

function download(url, redirects = 0) {
  return new Promise((resolve, reject) => {
    if (redirects > 5) return reject(new Error('重定向过多'));
    https
      .get(url, { headers: { 'user-agent': 'wxapkg-killer-mcp' } }, (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          res.resume();
          return download(res.headers.location, redirects + 1).then(resolve, reject);
        }
        if (res.statusCode !== 200) {
          res.resume();
          return reject(new Error(`HTTP ${res.statusCode}: ${url}`));
        }
        const chunks = [];
        res.on('data', (c) => chunks.push(c));
        res.on('end', () => resolve(Buffer.concat(chunks)));
      })
      .on('error', reject);
  });
}

function extractTarGz(buf, destDir, wantName) {
  let offset = 0;
  let found = false;
  while (offset + 512 <= buf.length) {
    const header = buf.subarray(offset, offset + 512);
    if (header[0] === 0) break;
    const name = header.subarray(0, 100).toString('utf8').replace(/\0.*$/, '');
    const sizeStr = header.subarray(124, 136).toString('utf8').replace(/\0.*$/, '').trim();
    const size = parseInt(sizeStr, 8) || 0;
    const type = String.fromCharCode(header[156]);
    const bodyStart = offset + 512;
    if (type === '0' || type === '\0') {
      if (path.basename(name) === wantName) {
        fs.writeFileSync(path.join(destDir, wantName), buf.subarray(bodyStart, bodyStart + size));
        found = true;
        break;
      }
    }
    offset = bodyStart + Math.ceil(size / 512) * 512;
  }
  if (!found) throw new Error(`压缩包内未找到 ${wantName}`);
}

function extractZip(buf, destDir, wantName) {
  let offset = 0;
  while (offset + 30 <= buf.length) {
    if (buf.readUInt32LE(offset) !== 0x04034b50) break;
    const method = buf.readUInt16LE(offset + 8);
    const compSize = buf.readUInt32LE(offset + 18);
    const nameLen = buf.readUInt16LE(offset + 26);
    const extraLen = buf.readUInt16LE(offset + 28);
    const name = buf.subarray(offset + 30, offset + 30 + nameLen).toString('utf8');
    const dataStart = offset + 30 + nameLen + extraLen;
    const dataEnd = dataStart + compSize;
    if (path.basename(name) === wantName) {
      const raw = buf.subarray(dataStart, dataEnd);
      const body = method === 0 ? raw : method === 8 ? zlib.inflateRawSync(raw) : null;
      if (!body) throw new Error(`不支持的 zip 压缩方法 ${method}`);
      fs.writeFileSync(path.join(destDir, wantName), body);
      return;
    }
    offset = dataEnd;
  }
  throw new Error(`压缩包内未找到 ${wantName}`);
}

function expectedSHA(text, filename) {
  for (const line of text.split(/\r?\n/)) {
    const m = line.match(/^([a-fA-F0-9]{64})\s+\*?(\S+)\s*$/);
    if (m && m[2] === filename) return m[1].toLowerCase();
  }
  return '';
}

async function main() {
  if (process.env.WXAPKG_KILLER_SKIP_DOWNLOAD === '1') {
    console.log('[wxapkg-killer] 已跳过二进制下载（WXAPKG_KILLER_SKIP_DOWNLOAD=1）');
    return;
  }
  const info = platformInfo();
  if (!info) {
    throw new Error(`不支持的平台 ${process.platform}/${process.arch}。请从源码构建：go build -o wxapkg-killer .`);
  }

  const asset = `wxapkg-killer-${info.os}-${info.arch}`;
  const archiveName = asset + info.ext;
  const innerName = info.os === 'windows' ? `${asset}.exe` : asset;
  const base =
    VERSION === 'latest'
      ? `https://github.com/${REPO}/releases/latest/download`
      : `https://github.com/${REPO}/releases/download/${VERSION}`;

  const vendor = path.join(__dirname, '..', 'vendor');
  const target = path.join(vendor, process.platform === 'win32' ? 'wxapkg-killer.exe' : 'wxapkg-killer');
  fs.mkdirSync(vendor, { recursive: true });

  console.log(`[wxapkg-killer] 下载 ${base}/${archiveName}`);
  const buf = await download(`${base}/${archiveName}`);

  try {
    const sumBuf = await download(`${base}/checksums.txt`);
    const expect = expectedSHA(sumBuf.toString('utf8'), archiveName);
    if (!expect) throw new Error(`checksums.txt 中没有 ${archiveName}`);
    const actual = crypto.createHash('sha256').update(buf).digest('hex');
    if (expect !== actual) {
      throw new Error(`SHA256 校验不通过（期望 ${expect}，实际 ${actual}）`);
    }
    console.log('[wxapkg-killer] SHA256 校验通过');
  } catch (e) {
    if (/HTTP 404/.test(e.message) && e.message.includes('checksums.txt')) {
      console.warn('[wxapkg-killer] 未找到 checksums.txt，跳过校验');
    } else {
      throw e;
    }
  }

  const staging = fs.mkdtempSync(path.join(os.tmpdir(), 'wxapkg-killer-'));
  try {
    if (info.ext === '.zip') {
      try {
        extractZip(buf, staging, innerName);
      } catch (err) {
        const zipPath = path.join(staging, archiveName);
        fs.writeFileSync(zipPath, buf);
        execFileSync('tar', ['-xf', zipPath, '-C', staging], { stdio: 'inherit' });
        if (!fs.existsSync(path.join(staging, innerName))) throw err;
      }
    } else {
      extractTarGz(zlib.gunzipSync(buf), staging, innerName);
    }
    fs.copyFileSync(path.join(staging, innerName), target);
  } finally {
    fs.rmSync(staging, { recursive: true, force: true });
  }
  fs.chmodSync(target, 0o755);
  console.log(`[wxapkg-killer] 已安装到 ${target}`);
}

main().catch((e) => {
  console.error(`[wxapkg-killer] postinstall 失败：${e.message}`);
  console.error('[wxapkg-killer] 可从 https://github.com/' + REPO + '/releases 手动下载，并设 WXAPKG_KILLER_BIN 指向它');
  process.exit(1);
});
