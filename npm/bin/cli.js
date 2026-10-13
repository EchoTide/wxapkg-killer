#!/usr/bin/env node
'use strict';

// wxapkg-killer 的 npm 包装入口。
// 优先级：环境变量 WXAPKG_KILLER_BIN > 随包下载的 vendor 二进制 > 系统 PATH

const { spawnSync } = require('child_process');
const fs = require('fs');
const path = require('path');

const exeName = process.platform === 'win32' ? 'wxapkg-killer.exe' : 'wxapkg-killer';

function run(bin) {
  const r = spawnSync(bin, process.argv.slice(2), { stdio: 'inherit' });
  if (r.error) return false;
  process.exit(r.status === null ? 1 : r.status);
}

function main() {
  const candidates = [];
  if (process.env.WXAPKG_KILLER_BIN) candidates.push(process.env.WXAPKG_KILLER_BIN);
  candidates.push(path.join(__dirname, '..', 'vendor', exeName));
  candidates.push(exeName); // 交给系统 PATH

  for (const c of candidates) {
    if (c === exeName) {
      run(c); // PATH 查找，spawnSync 自行处理
    }
    if (fs.existsSync(c)) {
      run(c);
    }
  }

  console.error('\n未找到 wxapkg-killer 二进制。可任选其一：');
  console.error('  npx wxapkg-killer-mcp        （重新安装本包）');
  console.error('  npm i -g wxapkg-killer-mcp');
  console.error('  或从 https://github.com/EchoTide/wxapkg-killer/releases 下载后设置 WXAPKG_KILLER_BIN');
  process.exit(1);
}

main();
