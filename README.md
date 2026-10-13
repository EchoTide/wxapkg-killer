# wxapkg-killer

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](./LICENSE)
[![Release](https://github.com/EchoTide/wxapkg-killer/actions/workflows/release.yml/badge.svg)](https://github.com/EchoTide/wxapkg-killer/actions/workflows/release.yml)

**English.** MCP server that unpacks WeChat mini-program and mini-game `.wxapkg` packages from your local cache, so an AI agent can read and analyze the code for security research, feature analysis, and self-audit. Analyze only apps you own or are authorized to test. It does not upload anything. Tools run with the current user's permissions and are not a sandbox.

把微信小程序 / 小游戏在本机缓存的 `.wxapkg` 解密、解包，并以 **MCP Server** 的形式交给 Agent。解包之后，源码、配置和资源都在本地目录里，Agent 可以直接读、搜、分析，例如：

- **安全研究**：找硬编码的密钥、接口域名、调试开关、未下线的管理入口
- **功能分析**：梳理页面结构、分包划分、调用的 `wx.*` 接口和后端接口
- **引擎识别**：判断是 Cocos、Laya、Egret、Unity、Taro 还是 uni-app（`wxapkg_audit` 第 1 节）
- **开发者自查**：看解包之后，别人能直接拿到什么

```
扫描本机缓存  →  解密解包  →  Agent 读取与分析  →  （可选）暴露面审计
```

不用点 GUI、不用记命令行，Agent 一条指令就能走完整条链路。

> **使用范围**：只分析你自己拥有、或已获得授权的小程序 / 小游戏。未经授权分析他人的小程序，可能侵犯著作权、违反微信平台规则，并可能触犯相关法律，后果由使用者自行承担。工具只做解密、解包和 JS/JSON/HTML 美化，不还原 WXML/WXSS，代码理解由 Agent 完成。

---

## 为什么需要它

微信小程序/小游戏缓存在本地的 `.wxapkg` 包，加密密钥就是 **wxid 本身**，
salt（`saltiest`）、IV（`the iv: 16 bytes`）、迭代次数（1000）全是硬编码常量。
所以只要包落到用户磁盘上，解密就是零成本的确定性操作。

这意味着**小程序的客户端代码和资源本质上可以被完整取出**。本项目把这一步做成 Agent 能调用的工具，取出之后的分析也交给 Agent：读代码、搜关键字、看资源结构。

---

## 安装

三种方式任选其一。装完执行一次 `wxapkg-killer setup`，它会自动探测本机的 Agent、
写入 MCP 配置，并把配套的**工作流指令**装进去——之后你在 Agent 里直接说人话就行。

### 方式一：一行命令（推荐）

**macOS / Linux**

```bash
curl -fsSL https://raw.githubusercontent.com/EchoTide/wxapkg-killer/main/install.sh | sh
```

**Windows（PowerShell）**

```powershell
irm https://raw.githubusercontent.com/EchoTide/wxapkg-killer/main/install.ps1 | iex
```

两个入口做的事一样：下载与平台匹配的预编译二进制 → 安装 → 调 `setup`。
只装不注册可加 `--no-setup`。

### 方式二：下载预编译二进制

从 [Releases](https://github.com/EchoTide/wxapkg-killer/releases) 下载对应平台的压缩包，
解压后执行 `wxapkg-killer setup`。

### 方式三：从源码构建

```bash
git clone https://github.com/EchoTide/wxapkg-killer.git
cd wxapkg-killer
go build -o wxapkg-killer .
```

要求 Go ≥ 1.25。

> 若 `goproxy.cn` 拉不到依赖，可切换代理：
> `export GOPROXY=https://proxy.golang.org,direct`

---

## 接入 Agent

```bash
wxapkg-killer setup
```

它会：

1. 探测本机已安装的 Agent（见下表）；
2. 预览将要写入的路径，**确认后才动手**；
3. 写入 MCP 配置——**保留原有内容和无关字段**，并先生成一份 `.wxapkg-killer.bak` 备份；
4. 把工作流指令 `SKILL.md` 装到各 Agent 的 skills 目录，教会 Agent 正确的调用链路。

| 客户端 id | 客户端 | 配置文件 |
| --- | --- | --- |
| `claude_code` | Claude Code | `~/.claude.json` |
| `codex` | Codex CLI | `~/.codex/config.toml` |
| `cursor` | Cursor | `~/.cursor/mcp.json` |
| `workbuddy` | WorkBuddy | `~/.workbuddy/mcp.json` |
| `gemini` | Gemini CLI | `~/.gemini/settings.json` |
| `windsurf` | Windsurf | `~/.codeium/windsurf/mcp_config.json` |
| `continue` | Continue | `~/.continue/config.json` |
| `claude_desktop` | Claude Desktop | 各平台应用数据目录 |
| `opencode` | OpenCode | `~/.config/opencode/opencode.json`（`mcp.servers`，不是 `mcpServers`） |

常用参数：

```bash
wxapkg-killer setup --dry-run                    # 只看计划，不写文件
wxapkg-killer setup --client codex --client cursor
wxapkg-killer setup --all-detected --yes --json  # 脚本化调用
wxapkg-killer setup --config ./my-mcp.json       # 逃生口：任意含 mcpServers 的 JSON
wxapkg-killer doctor                             # 只读自检
wxapkg-killer uninstall                          # 清干净
```

### 注册之后还差一步：信任

写盘**不等于**生效。部分 Agent 有安全门控，需要你手动确认后才会真正启动本 MCP：

| Agent | 注册后还需要 |
| --- | --- |
| **WorkBuddy** | 打开左侧「专家·技能·连接器」→ 连接器 → 右上角「自定义连接器」，在 MCP 服务管理里找到 `wxapkg-killer`，点击「**信任**」并启用；然后**新建一个会话**即可调用（旧会话不会热加载） |
| Claude Code / Claude Desktop | 重启后首次调用工具时会弹出信任确认，放行即可 |
| OpenCode | 重开一个会话即可，当前会话不会热加载 |
| 其余（Codex / Cursor / Gemini / Windsurf / Continue） | 重启即可 |

排查「工具列表里看不到 wxapkg」时，先跑 `wxapkg-killer doctor`——它会检查注册、技能安装和 WorkBuddy 的信任状态，并给出具体的待处理步骤。

### 手动配置

若偏好手写，配置条目长这样（`env` 可选）：

```json
{
  "mcpServers": {
    "wxapkg-killer": {
      "command": "H:/path/to/wxapkg-killer.exe",
      "args": ["serve"],
      "env": {
        "WXAPKG_OUT_ROOT": "D:/wxapkg-out"
      }
    }
  }
}
```

`WXAPKG_OUT_ROOT` 可选，用于指定解包输出根目录，默认为 `~/wxapkg-killer/out`。

OpenCode V2 的条目不同，写在 `mcp.servers` 里，`command` 是数组：

```jsonc
{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "wxapkg-killer": {
        "type": "local",
        "command": ["H:/path/to/wxapkg-killer.exe", "serve"]
      }
    }
  }
}
```

`wxapkg-killer setup --client opencode` 会合并进已有的 `opencode.json`（或仅存在的 `opencode.jsonc`），只改 `mcp.servers.wxapkg-killer`，不重排其它字段。已有配置会先备份为 `.wxapkg-killer.bak`。

---

## 工具列表

| 工具 | 作用 |
| --- | --- |
| `wxapkg_scan` | 扫描本机微信缓存，列出所有小程序的 **appid、大小、修改时间、路径**（按修改时间倒序，最近打开的在最前）。不传路径时自动探测微信 3.x / 4.x 默认缓存目录 |
| `wxapkg_unpack` | 解密并解包指定 appid（或指定 `.wxapkg` 路径），输出完整源码与资源目录，可开关解密与 js/json/html 美化 |
| `wxapkg_list_files` | 列出解包目录的文件，可按扩展名过滤、按体积排序 |
| `wxapkg_read_file` | 读取单个文件内容，支持 offset / max_bytes 分段读取，二进制返回头部摘要 |
| `wxapkg_search` | 在解包目录中做正则搜索（自动跳过二进制），支持文件过滤与按文件统计 |
| `wxapkg_audit` | 一键暴露面审计，输出结构化风险报告（详见下） |

### `wxapkg_audit` 检查项

| 章节 | 内容 |
| --- | --- |
| 技术栈识别 | Cocos / Laya / Egret / Unity / Taro / uni-app / WePY / mpvue 等，附证据文件 |
| 后端接口域名 | 提取所有业务域名并统计出现次数；`github.com`、`apple.com`、微信 CDN 等公共域名自动归入「已忽略」折叠区 |
| 敏感信息扫描 | 硬编码 AES 密钥、硬编码凭据、私钥、云开发直连、调试开关、console 输出、管理入口 |
| 微信 API 使用面 | 统计用到的 `wx.*` 接口及调用次数 |
| 分包完整性 | 对比 `app-config.json` 声明的分包与本地实际缓存，列出缺失项 |
| 代码混淆程度 | 逐文件统计标识符平均长度与含 `$` 比例，判断混淆情况 |

审计结果可通过 `save_report: true` 写成 `wxapkg-audit-report.md` 存到解包目录。

---

## 典型用法

在 Agent 里直接说：

> 扫一下我微信里的小程序，把最近打开的那个解包，然后梳理它的页面结构和分包划分。

Agent 会自行调用 `wxapkg_scan` → `wxapkg_unpack`，再用 `wxapkg_list_files` / `wxapkg_read_file` / `wxapkg_search` 读代码。

也可以单独问：

> 解包目录里有没有硬编码的密钥？ → `wxapkg_search` with `(?i)(secret|apikey)["']?\s*[:=]\s*["'][^"']{8,}["']`
>
> 这个包用的什么引擎？ → `wxapkg_audit` 第 1 节
>
> 它请求了哪些后端域名？ → `wxapkg_audit` 第 2 节
>
> 有哪些分包缓存没拉到？ → `wxapkg_audit` 第 5 节
>
> 做一次完整的暴露面审计，给出加固建议。 → `wxapkg_audit`

---

## 常见问题

**Q: 扫不到任何小程序？**

微信只在**你实际打开过**某个小程序后才把包缓存到本地。先在 PC 微信里打开目标小程序，再重新扫描。

**Q: 审计报 `分包缺失`？**

同上。微信 PC 端按入口粒度缓存分包，没点过的玩法不会下载。在微信里进一次对应入口，再重新解包即可补齐。

**Q: 支持哪些微信版本？**

微信 3.x（`Documents\WeChat Files`）与 4.x（`%APPDATA%\Tencent\xwechat\radium`）的自动探测都由上游 `wechat` 包提供。若自动探测失败，可手动指定 `path` 参数。

**Q: macOS / Linux 能用吗？**

自动探测路径的分支由上游包按平台编译提供，本工具本身是纯 Go 跨平台实现，编译即可用。预编译包覆盖 windows/amd64、windows/arm64、darwin/amd64、darwin/arm64、linux/amd64、linux/arm64。

**Q: macOS 提示「无法打开，因为无法验证开发者」？**

Release 二进制没有做 Apple 公证。确认文件来源后去掉隔离属性：

```bash
xattr -d com.apple.quarantine ~/.local/bin/wxapkg-killer
```

**Q: 这些工具会不会把文件传出去？会不会读到解包目录以外的文件？**

不会联网上传。但它们以当前用户权限读写本机路径，**不是沙箱**。`wxapkg_read_file` / `wxapkg_search` / `wxapkg_list_files` / `wxapkg_audit` 会访问调用方给出的路径；`wxapkg_unpack` 和解包报告会写文件。只在你信任的 Agent 里启用，并且只对自己拥有或已授权的小程序使用。

---

## 项目结构

```
.
├── main.go              入口 + 子命令分发（serve/setup/doctor/uninstall）
├── tools_scan.go        wxapkg_scan
├── tools_unpack.go      wxapkg_unpack
├── tools_inspect.go     list_files / read_file / search
├── tools_audit.go       wxapkg_audit
├── cmd_setup.go         客户端探测、配置合并、备份与原子写入
├── skills/              随二进制分发的 Agent 工作流指令（go:embed）
│   └── wxapkg-unpack/SKILL.md
├── install.sh           macOS / Linux 一行安装
├── install.ps1          Windows 一行安装
├── npm/                 npm 包装包（npx wxapkg-killer-mcp）
└── tests/               冒烟测试
```

## 技术说明

解密与 `.wxapkg` 容器解析复用 [wux1an/wxapkg](https://github.com/wux1an/wxapkg) 的 `wechat` 包
（以 Go module 依赖引入，**未复制其源码，行为与官方 GUI 工具一致**）。
本项目在此之上补充了面向 Agent 的检视与审计能力。

上游项目的 `wechat` 包不含 GUI 依赖，因此可独立复用。

## License

本项目采用 [MIT](./LICENSE) 协议。

> 注意：上游依赖 `github.com/wux1an/wxapkg` 目前**未声明开源协议**。
> 本项目的 MIT 许可**不覆盖**这段上游代码。发布的二进制会静态链接它。
> 本项目只把它当作外部 Go module 调用其导出 API，没有复制或修改其源码，也没有获得再分发授权。
> 若上游后续补充协议，以该协议为准；在此之前，使用或再分发二进制的风险由使用者自行承担。
> 详见 [NOTICE](./NOTICE)。
