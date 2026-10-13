package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/wux1an/wxapkg/wechat"
)

// registerUnpackTools 注册解包相关工具
func registerUnpackTools(s *server.MCPServer) {
	s.AddTool(mcp.NewTool("wxapkg_unpack",
		mcp.WithDescription(
			"解密并解包指定的微信小程序/小游戏，输出完整源码与资源目录。\n"+
				"密钥自动取 wxid（微信的 wxapkg 加密密钥就是 wxid 本身，salt/IV/迭代次数均为硬编码常量）。\n"+
				"解包后可用 wxapkg_list_files / wxapkg_read_file / wxapkg_search / wxapkg_audit 继续分析。"),
		mcp.WithString("wxid",
			mcp.Description("小程序 ID，形如 wx0123456789abcdef。与 source_path 二选一。")),
		mcp.WithString("source_path",
			mcp.Description("wxapkg 文件或其所在目录的绝对路径。与 wxid 二选一。")),
		mcp.WithString("output_dir",
			mcp.Description("输出目录。留空则自动使用 <输出根目录>/<wxid>_unpacked。")),
		mcp.WithBoolean("decrypt",
			mcp.Description("是否解密。默认 true（微信缓存的包基本都是加密的）。")),
		mcp.WithBoolean("beautify",
			mcp.Description("是否美化 js/json/html。默认 true，便于阅读与搜索。")),
	), handleUnpack)
}

// findItemByWxid 在缓存根目录中定位某个 wxid
// firstWxapkgItem 取出扫描结果的第一项。空结果必须报错，不能对 items[0] 取值。
func firstWxapkgItem(items []wechat.WxapkgItem) (*wechat.WxapkgItem, error) {
	if len(items) == 0 {
		return nil, fmt.Errorf("未找到可解包的 wxapkg 文件")
	}
	return &items[0], nil
}

func findItemByWxid(wxid string) (*wechat.WxapkgItem, string, error) {
	res := wechat.Platform.GetDefaultPaths()
	for _, root := range res.Paths {
		items, err := wechat.ScanWxapkgItem(root, true)
		if err != nil {
			continue
		}
		for i := range items {
			if strings.EqualFold(items[i].WxId, wxid) {
				return &items[i], root, nil
			}
		}
	}
	return nil, "", fmt.Errorf("在所有缓存目录中均未找到 wxid=%s 的包（可能未在微信中打开过）", wxid)
}

func handleUnpack(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	wxid := req.GetString("wxid", "")
	srcPath := req.GetString("source_path", "")
	outDir := req.GetString("output_dir", "")
	decrypt := req.GetBool("decrypt", true)
	beautify := req.GetBool("beautify", true)

	if wxid == "" && srcPath == "" {
		return mcp.NewToolResultError("必须提供 wxid 或 source_path 其中之一"), nil
	}

	var item *wechat.WxapkgItem
	var scanRoot string

	if srcPath != "" {
		if _, err := os.Stat(srcPath); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("source_path 不存在: %s", srcPath)), nil
		}
		items, err := wechat.ScanWxapkgItem(srcPath, false)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("读取失败: %v", err)), nil
		}
		picked, err := firstWxapkgItem(items)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("source_path 下没有找到 wxapkg 包: %s", srcPath)), nil
		}
		item = picked
		if wxid != "" {
			item.WxId = wxid
			item.EncryptKey = wxid
		}
	} else {
		found, root, err := findItemByWxid(wxid)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		item, scanRoot = found, root
	}

	if wxid == "" {
		wxid = item.WxId
	}
	if outDir == "" {
		name := wxid
		if name == "" || name == "unknown" {
			name = filepath.Base(item.Location)
		}
		outDir = filepath.Join(defaultOutRoot, name+"_unpacked")
	}
	// 必须规范化成绝对路径：上游校验用 strings.HasPrefix(savePath, UnpackSavePath)，
	// 若 SavePath 是相对路径或分隔符不一致（如传入 C:/a/b 而 Windows 上 Abs 为 C:\a\b），
	// 会被误判成目录穿越而解包失败。
	outDir, err := filepath.Abs(outDir)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("输出目录非法: %s (%v)", outDir, err)), nil
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("无法创建输出目录 %s: %v", outDir, err)), nil
	}

	opts := &wechat.UnpackOptions{
		EnableDecrypt:      decrypt,
		EnableJsBeautify:   beautify,
		EnableHtmlBeautify: beautify,
		EnableJsonBeautify: beautify,
		OutputDir:          outDir,
		SavePath:           outDir,
	}

	// 收集解包过程中的状态
	var mu sync.Mutex
	var lastMsg string
	unpacker := wechat.NewUnpacker(item, opts)
	unpacker.UnpackWithStatusCallback(func(it *wechat.WxapkgItem) {
		mu.Lock()
		defer mu.Unlock()
		if it.UnpackErrorMessage != "" {
			lastMsg = it.UnpackErrorMessage
		}
	})

	mu.Lock()
	errText := lastMsg
	status := item.UnpackStatus
	total := item.UnpackTotal
	mu.Unlock()

	if errText != "" || status == wechat.StatusTypeError {
		return mcp.NewToolResultError(fmt.Sprintf("解包失败: %s", errText)), nil
	}

	// 统计产出
	var fileCount, dirCount int
	var totalSize int64
	var topFiles []map[string]any
	type fi struct {
		p string
		s int64
	}
	var all []fi
	_ = filepath.Walk(outDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			dirCount++
			return nil
		}
		fileCount++
		totalSize += info.Size()
		all = append(all, fi{p, info.Size()})
		return nil
	})
	// 体积 Top 10
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[j].s > all[i].s {
				all[i], all[j] = all[j], all[i]
			}
		}
	}
	for i, f := range all {
		if i >= 10 {
			break
		}
		rel, _ := filepath.Rel(outDir, f.p)
		topFiles = append(topFiles, map[string]any{
			"path": rel,
			"size": humanSize(f.s),
		})
	}

	summary, _ := json.MarshalIndent(map[string]any{
		"wxid":       wxid,
		"scan_root":  scanRoot,
		"output_dir": outDir,
		"decrypted":  decrypt,
		"beautified": beautify,
		"files":      fileCount,
		"dirs":       dirCount,
		"total_size": humanSize(totalSize),
		"top_files":  topFiles,
	}, "", "  ")

	msg := "✅ 解包完成\n\n" + string(summary)
	msg += "\n\n下一步建议：\n"
	msg += "· wxapkg_audit —— 一键暴露面审计（硬编码密钥/接口域名/明文配置/分包缺口）\n"
	msg += "· wxapkg_list_files / wxapkg_read_file / wxapkg_search —— 按需下钻证据"
	if total > 0 {
		msg += fmt.Sprintf("\n（容器内共解析出 %d 个文件条目）", total)
	}

	return mcp.NewToolResultText(msg), nil
}
