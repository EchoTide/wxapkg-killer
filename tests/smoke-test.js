// wxapkg-killer MCP 冒烟测试
//
// 用法：
//   go build -o wxapkg-killer.exe ..      # 先构建
//   node tests/smoke-test.js [exe路径] [测试用解包目录]
//
// 不传参数时：exe 取上级目录的构建产物，
// 解包目录由 wxapkg_scan 结果自动推导（也可直接用 WXAPKG_TEST_DIR 指定）。
const { spawn } = require('child_process');
const path = require('path');
const os = require('os');

const EXE = process.argv[2] ||
  process.env.WXAPKG_EXE ||
  path.join(__dirname, '..', process.platform === 'win32' ? 'wxapkg-killer.exe' : 'wxapkg-killer');

let UNPACKED = process.argv[3] || process.env.WXAPKG_TEST_DIR || '';

const child = spawn(EXE, [], { stdio: ['pipe', 'pipe', 'pipe'] });

let buf = '';
const pending = new Map();
let nextId = 1;

child.stdout.on('data', (d) => {
  buf += d.toString();
  let nl;
  while ((nl = buf.indexOf('\n')) >= 0) {
    const line = buf.slice(0, nl).trim();
    buf = buf.slice(nl + 1);
    if (!line) continue;
    let msg;
    try { msg = JSON.parse(line); } catch { continue; }
    if (msg.id && pending.has(msg.id)) { pending.get(msg.id)(msg); pending.delete(msg.id); }
  }
});
child.stderr.on('data', (d) => { const s = d.toString().trim(); if (s) console.log('[stderr]', s); });
child.on('error', (e) => { console.error('启动失败:', e.message); process.exit(1); });

function send(method, params, isNotification = false) {
  return new Promise((resolve) => {
    const id = nextId++;
    const payload = { jsonrpc: '2.0', method, params };
    if (!isNotification) { payload.id = id; pending.set(id, resolve); }
    child.stdin.write(JSON.stringify(payload) + '\n');
    if (isNotification) resolve(null);
    setTimeout(() => { if (pending.has(id)) { pending.delete(id); resolve({ error: 'timeout' }); } }, 180000);
  });
}

const text = (r) => {
  if (!r || r.error) return 'ERROR: ' + JSON.stringify(r && r.error);
  const c = r.result && r.result.content;
  if (!c) return JSON.stringify(r.result).slice(0, 300);
  return c.map(x => x.text || '').join('\n');
};

const head = (s, n = 1200) => (s.length > n ? s.slice(0, n) + '\n...[截断]' : s);

(async () => {
  console.log('exe:', EXE);

  console.log('\n===== 1. initialize =====');
  const init = await send('initialize', {
    protocolVersion: '2024-11-05',
    capabilities: {},
    clientInfo: { name: 'smoke-test', version: '1.0.0' },
  });
  const si = init.result && init.result.serverInfo;
  console.log('server:', si ? `${si.name} v${si.version}` : '(未知)');
  await send('notifications/initialized', {}, true);

  console.log('\n===== 2. tools/list =====');
  const list = await send('tools/list', {});
  const tools = (list.result && list.result.tools) || [];
  console.log('工具数量:', tools.length);
  tools.forEach(t => console.log('  -', t.name.padEnd(20), (t.description || '').split('\n')[0].slice(0, 60)));

  const has = (n) => tools.some(t => t.name === n);
  const expect = ['wxapkg_scan', 'wxapkg_unpack', 'wxapkg_list_files', 'wxapkg_read_file', 'wxapkg_search', 'wxapkg_audit'];
  const missing = expect.filter(n => !has(n));
  if (missing.length) {
    console.log(`❌ 缺少工具: ${missing.join(', ')}`);
    child.kill();
    process.exit(1);
  }
  console.log('✅ 6 个通用工具齐全');
  const extra = tools.filter(t => !expect.includes(t.name)).map(t => t.name);
  if (extra.length) console.log('⚠️ 非预期工具（应已移除）:', extra.join(', '));

  console.log('\n===== 3. wxapkg_scan =====');
  const scanText = text(await send('tools/call', { name: 'wxapkg_scan', arguments: {} }));
  console.log(head(scanText, 800));

  if (!UNPACKED) {
    // 与工具默认输出根目录保持一致：~/wxapkg-killer/out/<wxid>_unpacked
    const wxidM = scanText.match(/"wxid":\s*"([^"]+)"/);
    if (wxidM) {
      UNPACKED = path.join(os.homedir(), 'wxapkg-killer', 'out', wxidM[1] + '_unpacked').replace(/\\/g, '/');
    }
  }
  console.log('\n后续测试使用目录:', UNPACKED || '(未确定)');

  if (!UNPACKED) {
    console.log('\n⚠️ 无法确定解包目录，跳过后续测试。');
    child.kill(); process.exit(0);
  }

  console.log('\n===== 4. wxapkg_unpack =====');
  const wxidMatch = UNPACKED.match(/(wx[0-9a-f]{16})/);
  if (wxidMatch) {
    console.log(head(text(await send('tools/call', {
      name: 'wxapkg_unpack',
      arguments: { wxid: wxidMatch[1], output_dir: UNPACKED },
    })), 1000));
  } else {
    console.log('(目录名中找不到 wxid，跳过)');
  }

  console.log('\n===== 5. wxapkg_list_files =====');
  console.log(head(text(await send('tools/call', {
    name: 'wxapkg_list_files',
    arguments: { dir: UNPACKED, ext: 'json', sort_by_size: true, limit: 8 },
  })), 600));

  console.log('\n===== 6. wxapkg_search =====');
  console.log(head(text(await send('tools/call', {
    name: 'wxapkg_search',
    arguments: { dir: UNPACKED, pattern: 'https?://[a-zA-Z0-9\\.\\-]+\\.[a-z]{2,}', max_matches: 5 },
  })), 600));

  console.log('\n===== 7. wxapkg_read_file =====');
  console.log(head(text(await send('tools/call', {
    name: 'wxapkg_read_file',
    arguments: { path: UNPACKED + '/app-config.json', max_bytes: 600 },
  })), 600));

  console.log('\n===== 8. wxapkg_audit =====');
  const auditText = text(await send('tools/call', {
    name: 'wxapkg_audit',
    arguments: { dir: UNPACKED, save_report: true },
  }));
  console.log('报告长度:', auditText.length);
  console.log('---- 摘要（技术栈 / 域名 / 结论）----');
  const sec = auditText.split(/\n(?=## )/).filter(s => /^## (1|2|7)\./.test(s));
  console.log(head(sec.join('\n'), 1800));

  child.kill();
  console.log('\n===== 测试结束 =====');
  process.exit(0);
})();
