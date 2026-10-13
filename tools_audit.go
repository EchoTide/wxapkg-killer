package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerAuditTools 注册暴露面审计工具
func registerAuditTools(s *server.MCPServer) {
	s.AddTool(mcp.NewTool("wxapkg_audit",
		mcp.WithDescription(
			"对指定目录做一键暴露面审计，输出结构化风险报告。以当前用户权限读盘，不是沙箱。\n"+
				"检查项：引擎识别、后端接口域名、硬编码密钥/凭据、明文数值配置、"+
				"微信 API 使用面、调试残留、分包完整性、代码混淆程度。\n"+
				"这是判断“被破解后能泄露什么”的主要依据。"),
		mcp.WithString("dir", mcp.Description("解包后的目录路径"), mcp.Required()),
		mcp.WithBoolean("save_report",
			mcp.Description("是否把报告写成 Markdown 文件保存到解包目录（wxapkg-audit-report.md）。默认 false。")),
	), handleAudit)
}

// 审计规则（不含域名与概率，这两类单独做去噪处理）
var auditRules = []struct {
	name    string
	pattern string
	why     string
	sev     string // 高危 / 中危 / 低危
}{
	{"硬编码 AES 密钥", `(?i)\b(aes|des|rsa|crypto)[\s_-]*(key|secret|iv)\b["']?\s*[:=]\s*["'][^"'\s]{8,}["']`, "客户端写死对称密钥，可被直接提取伪造请求", "高危"},
	{"硬编码凭据", `(?i)\b(secret|api[_-]?key|apikey|access[_-]?token|appkey|password|passwd|private[_-]?key)\b["']?\s*[:=]\s*["'][^"'\s]{8,}["']`, "凭据可被直接读取，用于伪造请求", "高危"},
	{"私钥内容", `-----BEGIN (RSA |EC |)PRIVATE KEY-----`, "私钥被打进包内", "高危"},
	{"调试开关/测试残留", `(?i)(debugger|__DEV__|isDebug|mockData|testMode|isTest|enableDebug|__TEST__)`, "开发期代码未剔除，可能留下后门或测试开关", "中危"},
	{"console 输出", `(?i)console\.(log|debug|warn|error|info)`, "生产包保留日志输出，会泄露内部状态与调用链（框架自身日志占多数，需人工甄别）", "低危"},
	{"管理/后台入口", `(?i)["'](admin|backend|internal|superuser|root)["']`, "可能残留管理入口或未下线的高权限分支", "中危"},
	{"云函数/数据库直连", `(?i)(wx\.cloud\.|cloudfunction|database\(\)|collection\()`, "直连云开发资源，可绕过业务服务端直接读写数据", "高危"},
}

// 常见的公共/标准域名，出现不构成暴露面
var publicDomainWhitelist = map[string]bool{
	"github.com": true, "githubusercontent.com": true, "gitlab.com": true,
	"apple.com": true, "w3.org": true, "adobe.com": true, "mozilla.org": true,
	"schema.org": true, "creativecommons.org": true, "xml.org": true,
	"cocos.com": true, "cocos-creator": true, "cocos2d-x.org": true,
	"unity.com": true, "unity3d.com": true, "jquery.com": true,
	"microsoft.com": true, "windows.com": true, "google.com": true,
	"gstatic.com": true, "googleapis.com": true, "npmjs.com": true,
	"nodejs.org": true, "typescriptlang.org": true, "vuejs.org": true,
	"example.com": true, "localhost": true, "127.0.0.1": true,
	"dragonbones.com": true, "git.io": true, "jsdelivr.net": true,
	"unpkg.com": true, "cdnjs.com": true, "sentry.io": true,
	"wxsdk.com": true, "weixin.qq.com": true, "qlogo.cn": true, "qpic.cn": true,
}

// 资源/引擎目录，其内的数值字段多为动画帧率等，不计入业务配置
var noiseDirs = []string{"import/", "native/", "cocos-js/", "chunks/", "assets/"}

// 引擎特征
var engineSignatures = []struct {
	name    string
	pattern string
}{
	{"Cocos Creator 3.x", `cocos-js|cc\.internal|cocos/core`},
	{"Cocos Creator 2.x", `cocos2d-js|cc\.director`},
	{"LayaAir", `laya\.|LayaAir|layaair`},
	{"Egret", `egret\.|Egret`},
	{"Unity (WebGL)", `UnityLoader|il2cpp|unityFramework`},
	{"Three.js", `THREE\.`},
	{"Spine 骨骼动画", `spine|Spine`},
	{"Taro", `taro(Taro)?|@tarojs`},
	{"uni-app", `uni-app|uniCloud|__uniConfig`},
	{"WePY", `wepy|WePY`},
	{"mpvue", `mpvue`},
}

func handleAudit(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dir := reqStr(req, "dir")
	saveReport := req.GetBool("save_report", false)

	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return mcp.NewToolResultError(fmt.Sprintf("目录不存在: %s", dir)), nil
	}

	// 本工具自己注入的适配层文件，不属于被审计对象
	injected := map[string]bool{
		"wx-shim.js": true, "game.html": true, "index.html": true,
		"server.py": true, "wxapkg-audit-report.md": true,
	}

	// 收集所有文本文件
	var files []string
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if injected[filepath.Base(p)] {
			return nil
		}
		if !isBinaryPath(p) && info.Size() <= 32*1024*1024 {
			files = append(files, p)
		}
		return nil
	})

	// 预读内容（限制总量，防止爆内存）
	contents := map[string][]byte{}
	var readBytes int64
	const budget = 200 * 1024 * 1024
	for _, p := range files {
		if readBytes > budget {
			break
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if !utf8Like(b) {
			continue
		}
		contents[p] = b
		readBytes += int64(len(b))
	}

	var sb strings.Builder
	sb.WriteString("# wxapkg 暴露面审计报告\n\n")
	sb.WriteString(fmt.Sprintf("目标目录: `%s`\n", dir))
	sb.WriteString(fmt.Sprintf("文本文件: %d 个，已载入 %.1f MB\n\n", len(contents), float64(readBytes)/1024/1024))

	// ---------- 1. 引擎识别 ----------
	sb.WriteString("## 1. 技术栈识别\n\n")
	foundEngine := false
	for _, sig := range engineSignatures {
		re := regexp.MustCompile(sig.pattern)
		for p, b := range contents {
			if re.Find(b) != nil {
				rel, _ := filepath.Rel(dir, p)
				sb.WriteString(fmt.Sprintf("- **%s** （证据文件: `%s`）\n", sig.name, filepath.ToSlash(rel)))
				foundEngine = true
				break
			}
		}
	}
	if !foundEngine {
		sb.WriteString("- 未识别出已知引擎（可能是自研框架或混淆较深）\n")
	}
	// 读取 settings.json / app-config.json 补充信息
	if b, ok := contents[filepath.Join(dir, "src", "settings.json")]; ok {
		var d map[string]any
		if json.Unmarshal(b, &d) == nil {
			if dr, ok := d["designResolution"]; ok {
				j, _ := json.Marshal(dr)
				sb.WriteString(fmt.Sprintf("- 设计分辨率: `%s`\n", string(j)))
			}
			if pf, ok := d["platform"]; ok {
				sb.WriteString(fmt.Sprintf("- 构建平台: `%v`\n", pf))
			}
			if v, ok := d["version"]; ok {
				sb.WriteString(fmt.Sprintf("- 引擎版本: `%v`\n", v))
			}
		}
	}
	if b, ok := contents[filepath.Join(dir, "game.json")]; ok {
		var d map[string]any
		if json.Unmarshal(b, &d) == nil {
			if d["deviceOrientation"] != nil {
				sb.WriteString(fmt.Sprintf("- 屏幕方向: `%v`\n", d["deviceOrientation"]))
			}
		}
	}
	sb.WriteString("\n")

	// ---------- 2. 后端接口域名（独立提取，去噪）----------
	sb.WriteString("## 2. 后端接口域名\n\n")
	domainRe := regexp.MustCompile(`https?://([a-zA-Z0-9\.\-]+\.[a-z]{2,})(:\d+)?(/[^\s"'` + "`" + `<>]*)?`)
	ipRe := regexp.MustCompile(`https?://(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})(:\d+)?`)
	domainHits := map[string]int{}
	domainSample := map[string]string{}
	ipHits := map[string]int{}
	for p, b := range contents {
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		for _, m := range domainRe.FindAllSubmatch(b, -1) {
			d := strings.ToLower(string(m[1]))
			// 去掉 www. 前缀便于归并
			d = strings.TrimPrefix(d, "www.")
			domainHits[d]++
			if domainSample[d] == "" {
				idx := bytes.Index(b, m[0])
				if idx < 0 {
					idx = 0
				}
				start := idx - 50
				if start < 0 {
					start = 0
				}
				end := idx + 160
				if end > len(b) {
					end = len(b)
				}
				domainSample[d] = collapseSpaces(strings.ReplaceAll(string(b[start:end]), "\n", " "))
			}
		}
		for _, m := range ipRe.FindAllSubmatch(b, -1) {
			ip := string(m[1])
			if ip == "127.0.0.1" || ip == "0.0.0.0" {
				continue
			}
			ipHits[ip]++
		}
	}

	// 分成业务域名与公共域名
	type dm struct {
		d string
		c int
	}
	var bizDoms, pubDoms []dm
	for d, c := range domainHits {
		if isPublicDomain(d) {
			pubDoms = append(pubDoms, dm{d, c})
		} else {
			bizDoms = append(bizDoms, dm{d, c})
		}
	}
	sort.Slice(bizDoms, func(i, j int) bool { return bizDoms[i].c > bizDoms[j].c })

	if len(bizDoms) == 0 {
		sb.WriteString("未发现业务域名。\n")
	} else {
		sb.WriteString(fmt.Sprintf("**发现 %d 个业务域名**（这些是真实暴露的后端地址，可直接构造请求、被扫描）：\n\n", len(bizDoms)))
		sb.WriteString("| 域名 | 出现次数 |\n| --- | --- |\n")
		for _, x := range bizDoms {
			sb.WriteString(fmt.Sprintf("| `%s` | %d |\n", x.d, x.c))
		}
		sb.WriteString("\n上下文样例：\n\n")
		for i, x := range bizDoms {
			if i >= 3 {
				break
			}
			sb.WriteString(fmt.Sprintf("- `%s`\n  ```\n  %s\n  ```\n", x.d, truncate(domainSample[x.d], 240)))
		}
	}
	if len(ipHits) > 0 {
		sb.WriteString("\n**IP 直连**（绕过域名直接暴露源站）：\n")
		for ip, c := range ipHits {
			sb.WriteString(fmt.Sprintf("- `%s` ×%d\n", ip, c))
		}
	}
	if len(pubDoms) > 0 {
		sb.WriteString(fmt.Sprintf("\n<details><summary>已忽略 %d 个公共域名（引擎/标准库引用，非暴露面）</summary>\n\n", len(pubDoms)))
		for _, x := range pubDoms {
			sb.WriteString(fmt.Sprintf("- `%s`\n", x.d))
		}
		sb.WriteString("\n</details>\n")
	}
	sb.WriteString("\n")

	// ---------- 3. 规则扫描 ----------
	sb.WriteString("## 3. 敏感信息扫描\n\n")
	type finding struct {
		rule, sev, file, text string
		count                 int
	}
	var findings []finding
	for _, r := range auditRules {
		re, err := regexp.Compile(r.pattern)
		if err != nil {
			continue
		}
		fileHits := map[string]int{}
		sample := map[string]string{}
		total := 0
		// 数值类规则跳过引擎资源目录，否则 Cocos 动画的 rate 字段会造成大量误报
		skipNoise := r.name == "抽卡/掉落概率" || r.name == "奖励/货币数值"
		for p, b := range contents {
			relTmp, _ := filepath.Rel(dir, p)
			relTmp = filepath.ToSlash(relTmp)
			if skipNoise && isNoisePath(relTmp) {
				continue
			}
			locs := re.FindAllIndex(b, -1)
			if len(locs) == 0 {
				continue
			}
			rel := relTmp
			fileHits[rel] += len(locs)
			total += len(locs)
			if sample[rel] == "" {
				s, e := locs[0][0]-40, locs[0][1]+60
				if s < 0 {
					s = 0
				}
				if e > len(b) {
					e = len(b)
				}
				sample[rel] = collapseSpaces(strings.ReplaceAll(string(b[s:e]), "\n", " "))
			}
		}
		if total == 0 {
			continue
		}
		// 每个规则展示命中最多的几个文件
		type fc struct {
			f string
			c int
		}
		var cs []fc
		for f, c := range fileHits {
			cs = append(cs, fc{f, c})
		}
		sort.Slice(cs, func(i, j int) bool { return cs[i].c > cs[j].c })
		for i, c := range cs {
			if i >= 4 {
				break
			}
			findings = append(findings, finding{
				rule: r.name, sev: r.sev, file: c.f,
				text: sample[c.f], count: c.c,
			})
		}
		sb.WriteString(fmt.Sprintf("### %s 【%s】 — 共 %d 处\n", r.name, r.sev, total))
		sb.WriteString(fmt.Sprintf("风险: %s\n\n", r.why))
		for i, c := range cs {
			if i >= 4 {
				sb.WriteString(fmt.Sprintf("- 其余 %d 个文件…\n", len(cs)-4))
				break
			}
			sb.WriteString(fmt.Sprintf("- `%s` ×%d\n  ```\n  %s\n  ```\n", c.f, c.c, truncate(sample[c.f], 220)))
		}
		sb.WriteString("\n")
	}
	if len(findings) == 0 {
		sb.WriteString("未命中任何规则（可能是混淆彻底，或包内确实干净）。\n\n")
	}

	// ---------- 4. 微信 API 使用面 ----------
	sb.WriteString("## 4. 微信 API 使用面\n\n")
	wxAPI := map[string]int{}
	wxRe := regexp.MustCompile(`\bwx\.([a-zA-Z_$][a-zA-Z0-9_$]*)`)
	for _, b := range contents {
		for _, m := range wxRe.FindAllSubmatch(b, -1) {
			wxAPI[string(m[1])]++
		}
	}
	type wa struct {
		k string
		c int
	}
	var was []wa
	for k, c := range wxAPI {
		was = append(was, wa{k, c})
	}
	sort.Slice(was, func(i, j int) bool { return was[i].c > was[j].c })
	if len(was) == 0 {
		sb.WriteString("未发现 wx.* 调用。\n")
	} else {
		sb.WriteString(fmt.Sprintf("共用到 %d 个微信 API（Top 25）：\n\n", len(was)))
		sb.WriteString("| API | 次数 |\n| --- | --- |\n")
		for i, w := range was {
			if i >= 25 {
				sb.WriteString(fmt.Sprintf("| … | 其余 %d 个 |", len(was)-25))
				break
			}
			sb.WriteString(fmt.Sprintf("| `wx.%s` | %d |\n", w.k, w.c))
		}
	}
	sb.WriteString("\n")

	// ---------- 5. 分包完整性 ----------
	sb.WriteString("## 5. 分包完整性\n\n")
	cfgPath := filepath.Join(dir, "app-config.json")
	var declared []string
	if b, err := os.ReadFile(cfgPath); err == nil {
		var d struct {
			SubPackages []struct {
				Name string `json:"name"`
				Root string `json:"root"`
			} `json:"subPackages"`
		}
		if json.Unmarshal(b, &d) == nil {
			for _, sp := range d.SubPackages {
				n := sp.Name
				if n == "" {
					n = strings.Trim(strings.TrimSpace(sp.Root), "/")
				}
				declared = append(declared, n)
			}
		}
	}
	if len(declared) == 0 {
		sb.WriteString("未读取到 app-config.json 中的分包声明（可能是单包结构）。\n")
	} else {
		var have, missing []string
		for _, n := range declared {
			if st, err := os.Stat(filepath.Join(dir, "subpackages", n)); err == nil && st.IsDir() {
				have = append(have, n)
			} else {
				missing = append(missing, n)
			}
		}
		sb.WriteString(fmt.Sprintf("声明 %d 个分包，本地缓存 %d 个，缺失 %d 个。\n\n",
			len(declared), len(have), len(missing)))
		if len(missing) > 0 {
			sb.WriteString("**缺失分包**（微信只缓存实际打开过的入口；在 PC 微信里点一遍对应玩法再重新解包即可补全）：\n")
			for _, m := range missing {
				sb.WriteString(fmt.Sprintf("- `%s`\n", m))
			}
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\n")

	// ---------- 6. 混淆程度 ----------
	sb.WriteString("## 6. 代码混淆程度\n\n")
	identRe := regexp.MustCompile(`\b[A-Za-z_$][A-Za-z0-9_$]*\b`)
	type obfStat struct {
		file   string
		avgLen float64
		dollar float64 // 含 $ 的标识符比例
		long   int     // 长度 >=12 的标识符数（采样内）
		obf    bool
	}
	var stats []obfStat
	for p, b := range contents {
		if !strings.HasSuffix(p, ".js") || len(b) > 8*1024*1024 {
			continue
		}
		// 只采样前 1.5 MB，够统计且省时
		sample := b
		if len(sample) > 1536*1024 {
			sample = sample[:1536*1024]
		}
		ids := identRe.FindAll(sample, 4000)
		if len(ids) < 200 {
			continue
		}
		var sumLen, dollarCnt, longCnt int
		for _, id := range ids {
			s := string(id)
			sumLen += len(s)
			if strings.ContainsRune(s, '$') {
				dollarCnt++
			}
			if len(s) >= 12 {
				longCnt++
			}
		}
		avg := float64(sumLen) / float64(len(ids))
		dr := float64(dollarCnt) / float64(len(ids))
		rel, _ := filepath.Rel(dir, p)
		stats = append(stats, obfStat{
			file:   filepath.ToSlash(rel),
			avgLen: avg,
			dollar: dr,
			long:   longCnt,
			obf:    avg < 9 || dr > 0.12,
		})
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].file < stats[j].file })

	if len(stats) == 0 {
		sb.WriteString("未找到足够的 js 样本。\n\n")
	} else {
		sb.WriteString("| 文件 | 标识符平均长度 | 含 `$` 比例 | 长名(≥12)数 | 判断 |\n| --- | --- | --- | --- | --- |\n")
		obfCnt := 0
		for _, s := range stats {
			if s.obf {
				obfCnt++
			}
			sb.WriteString(fmt.Sprintf("| `%s` | %.1f | %.1f%% | %d | %s |\n",
				s.file, s.avgLen, s.dollar*100, s.long, map[bool]string{true: "已混淆", false: "可读"}[s.obf]))
		}
		sb.WriteString("\n")
		if obfCnt*2 >= len(stats) {
			sb.WriteString(fmt.Sprintf("**整体已混淆**（%d/%d 个文件）。变量名被压缩，阅读成本提高。\n", obfCnt, len(stats)))
		} else if obfCnt > 0 {
			sb.WriteString(fmt.Sprintf("**部分混淆**（%d/%d 个文件）。业务代码可读，是主要泄露面。\n", obfCnt, len(stats)))
		} else {
			sb.WriteString(fmt.Sprintf("**基本未混淆**（%d/%d 个文件均保留可读标识符）。\n", obfCnt, len(stats)))
			sb.WriteString("业务逻辑接近源码，可直接阅读。强烈建议使用官方《游戏深度保护》插件加固。\n")
		}
		sb.WriteString("> 注意：无论是否混淆，**字符串常量、接口地址、数值配置始终是明文**，混淆只能抬高阅读成本，不能防住提取。\n")
	}
	sb.WriteString("\n")

	// ---------- 7. 结论 ----------
	sb.WriteString("## 7. 结论与加固优先级\n\n")
	highCnt := 0
	for _, f := range findings {
		if f.sev == "高危" {
			highCnt++
		}
	}
	sb.WriteString(fmt.Sprintf("发现 **%d** 项高危暴露（共 %d 项规则命中）", highCnt, len(findings)))
	if len(bizDoms) > 0 {
		sb.WriteString(fmt.Sprintf("，以及 **%d 个业务后端域名**", len(bizDoms)))
	}
	sb.WriteString("。\n\n")
	sb.WriteString("| 优先级 | 措施 | 能防住什么 |\n| --- | --- | --- |\n")
	sb.WriteString("| P0 | 敏感逻辑下沉服务端：支付、下单、结算、权限校验一律服务端裁决，前端只上报原始输入 | 改包、伪造请求、越权 |\n")
	sb.WriteString("| P1 | 启用代码加固：微信官方《代码深度保护》/《游戏深度保护》插件（混淆、水印、代码锁、WASM 加固） | 静态阅读与二次分发 |\n")
	sb.WriteString("| P1 | 移除硬编码密钥；后端域名与 secret 运行时动态下发，接口补签名与限流 | 密钥与接口被直接提取 |\n")
	sb.WriteString("| P2 | 敏感配置加密下发；图片等资源走 CDN 签名 URL + 有效期 | 扒配置、盗用素材 |\n")
	sb.WriteString("| P2 | 提审前剔除 Mock 代码、调试开关、后台管理入口 | 后门与测试分支 |\n")
	sb.WriteString("\n> 客户端代码与资源本质上可被完整还原（本工具即证明）。\n")
	sb.WriteString("> 唯一真正兜底的是 **P0：服务端裁决**。\n")

	report := sb.String()

	if saveReport {
		out := filepath.Join(dir, "wxapkg-audit-report.md")
		if err := os.WriteFile(out, []byte(report), 0o644); err == nil {
			report += fmt.Sprintf("\n\n---\n报告已保存至: `%s`\n", out)
		} else {
			report += fmt.Sprintf("\n\n(报告保存失败: %v)\n", err)
		}
	}

	return mcp.NewToolResultText(report), nil
}

// isPublicDomain 判断域名是否属于公共/引擎引用（含子域后缀匹配）
func isPublicDomain(d string) bool {
	if publicDomainWhitelist[d] {
		return true
	}
	for k := range publicDomainWhitelist {
		if strings.HasSuffix(d, "."+k) {
			return true
		}
	}
	return false
}

// isNoisePath 判断是否为引擎资源目录（这些目录下的数值字段多为动画帧率等，非业务配置）
func isNoisePath(rel string) bool {
	lower := strings.ToLower(rel)
	for _, d := range noiseDirs {
		if strings.Contains(lower, d) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + " …"
}
