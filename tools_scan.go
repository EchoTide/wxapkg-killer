package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/wux1an/wxapkg/wechat"
)

// registerScanTools 注册扫描相关工具
func registerScanTools(s *server.MCPServer) {
	// ---------- wxapkg_scan ----------
	s.AddTool(mcp.NewTool("wxapkg_scan",
		mcp.WithDescription(
			"扫描本机微信小程序/小游戏缓存，列出所有 wxapkg 包。"+
				"不指定 path 时自动探测微信 3.x / 4.x 的默认缓存目录。"+
				"返回 wxid、大小、修改时间、路径，按修改时间倒序（最近打开的排最前）。"),
		mcp.WithString("path",
			mcp.Description("要扫描的目录。留空则自动探测微信默认缓存目录。")),
		mcp.WithBoolean("auto_detect",
			mcp.Description("是否打印自动探测过程的日志。默认 false。")),
	), handleScan)
}

type scanEntry struct {
	WxID       string `json:"wxid"`
	Size       int64  `json:"size"`
	SizeHuman  string `json:"size_human"`
	ModifyTime string `json:"modify_time"`
	Path       string `json:"path"`
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for i := n / unit; i >= unit; i /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func handleScan(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	path := req.GetString("path", "")
	showLog := req.GetBool("auto_detect", false)

	var logs string
	if path == "" {
		res := wechat.Platform.GetDefaultPaths()
		logs = res.Logs
		if len(res.Paths) == 0 {
			return mcp.NewToolResultError(
				"未能自动探测到微信小程序缓存目录。\n探测日志：\n" + logs +
					"\n请手动指定 path 参数（形如 ...\\xwechat\\radium\\users\\<hash>\\applet\\packages）"), nil
		}
		path = res.Paths[0]
	}

	if _, err := os.Stat(path); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("目录不存在或无法访问: %s (%v)", path, err)), nil
	}

	items, err := wechat.ScanWxapkgItem(path, true)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("扫描失败: %v", err)), nil
	}
	if len(items) == 0 {
		return mcp.NewToolResultText(fmt.Sprintf(
			"目录 %s 下没有找到任何 wxapkg 包。\n（微信只缓存你实际打开过的小程序）", path)), nil
	}

	// 按修改时间倒序
	sort.Slice(items, func(i, j int) bool {
		return items[i].LastModifyTime > items[j].LastModifyTime
	})

	var entries []scanEntry
	for _, it := range items {
		t := "-"
		if it.LastModifyTime > 0 {
			t = time.Unix(it.LastModifyTime, 0).Format("2006-01-02 15:04:05")
		}
		entries = append(entries, scanEntry{
			WxID:       it.WxId,
			Size:       it.Size,
			SizeHuman:  humanSize(it.Size),
			ModifyTime: t,
			Path:       it.Location,
		})
	}

	out, _ := json.MarshalIndent(map[string]any{
		"scan_root": path,
		"count":     len(entries),
		"items":     entries,
	}, "", "  ")

	msg := "扫描目录: " + path + "\n"
	if showLog && logs != "" {
		msg += "\n[自动探测日志]\n" + logs + "\n"
	}
	msg += "共 " + strconv.Itoa(len(entries)) + " 个小程序/小游戏（按修改时间倒序，最近打开的在最前）：\n\n"
	msg += string(out)
	msg += "\n\n下一步：用 wxapkg_unpack 并传入 wxid 解包。"

	return mcp.NewToolResultText(msg), nil
}
