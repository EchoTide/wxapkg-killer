// wxapkg-killer —— 微信小程序/小游戏解包 MCP Server
//
// 把 wux1an/wxapkg 的解包能力暴露成 MCP 工具，让 Agent 能自主完成
// 「扫描缓存 -> 解密解包 -> 检视文件 -> 暴露面审计」整条链路。
//
// 解密与容器解析复用原项目的 wechat 包，行为与官方 GUI 工具一致。
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// version 版本号。发布构建用 -ldflags "-X main.version=vX.Y.Z" 注入 tag；
// 本地构建未注入时为 dev。
var version = "dev"

// 默认解包输出根目录：~/wxapkg-killer/out（可用 WXAPKG_OUT_ROOT 覆盖）
var defaultOutRoot = func() string {
	if v := os.Getenv("WXAPKG_OUT_ROOT"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", "wxapkg-killer", "out")
	}
	return filepath.Join(home, "wxapkg-killer", "out")
}()

// reqStr 取必填字符串参数；缺失时返回空串（由各 handler 自行校验）
func reqStr(req mcp.CallToolRequest, key string) string {
	v, err := req.RequireString(key)
	if err != nil {
		return ""
	}
	return v
}

// dispatch 处理子命令；返回是否需要继续以 MCP Server 模式运行
func dispatch(args []string) bool {
	if len(args) == 0 {
		return true // 无参数 = 直接跑 MCP Server
	}
	switch args[0] {
	case "serve", "mcp":
		return true
	case "setup":
		os.Exit(runSetup(args[1:]))
	case "doctor", "status":
		os.Exit(runDoctor(args[1:]))
	case "uninstall", "remove":
		os.Exit(runUninstall(args[1:]))
	case "version", "--version", "-v":
		fmt.Println(version)
		os.Exit(0)
	case "help", "-h", "--help":
		printUsage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "未知命令: %s\n\n", args[0])
		printUsage()
		os.Exit(2)
	}
	return false
}

func main() {
	if !dispatch(os.Args[1:]) {
		return
	}

	s := server.NewMCPServer(
		"wxapkg-killer",
		version,
		server.WithToolCapabilities(true),
		server.WithInstructions(
			"微信小程序/小游戏解包工具链。\n"+
				"典型链路：wxapkg_scan（列出本机缓存的所有小程序 appid/时间/路径）"+
				"-> wxapkg_unpack（解密解包）-> wxapkg_list_files / wxapkg_read_file / wxapkg_search（检视）"+
				"-> wxapkg_audit（暴露面审计）。\n"+
				"不联网上传任何数据。工具以当前用户权限读写本机文件，不是沙箱："+
				"解包和 save_report 会写文件，检视类工具可读取调用方给出的任意本机路径。"+
				"仅用于对自己拥有或已授权的小程序做安全自查。"),
	)

	registerScanTools(s)
	registerUnpackTools(s)
	registerInspectTools(s)
	registerAuditTools(s)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintln(os.Stderr, "wxapkg-killer 退出:", err)
		os.Exit(1)
	}
}
