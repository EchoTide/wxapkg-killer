package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// registerInspectTools 注册文件查看类工具
func registerInspectTools(s *server.MCPServer) {

	// ---------- wxapkg_list_files ----------
	s.AddTool(mcp.NewTool("wxapkg_list_files",
		mcp.WithDescription(
			"列出解包目录中的文件。可按扩展名过滤、按体积排序，用于快速摸清包的结构。"),
		mcp.WithString("dir", mcp.Description("解包后的目录路径"), mcp.Required()),
		mcp.WithString("ext",
			mcp.Description("按扩展名过滤，多个用逗号分隔，如 js,json,astc。留空表示全部。")),
		mcp.WithBoolean("sort_by_size",
			mcp.Description("是否按体积倒序。默认 false（按路径排序）。")),
		mcp.WithNumber("limit", mcp.Description("最多返回多少条，默认 200。")),
	), handleListFiles)

	// ---------- wxapkg_read_file ----------
	s.AddTool(mcp.NewTool("wxapkg_read_file",
		mcp.WithDescription(
			"读取本机文件内容。文本文件直接返回内容，二进制文件返回头部摘要。"+
				"超出 max_bytes 会截断并提示。以当前用户权限读取，不限制在解包目录内，也不是沙箱。"),
		mcp.WithString("path", mcp.Description("文件绝对路径或相对于 dir 的路径"), mcp.Required()),
		mcp.WithNumber("max_bytes", mcp.Description("最多读取多少字节，默认 20000。")),
		mcp.WithNumber("offset", mcp.Description("从第几个字节开始读，默认 0。")),
	), handleReadFile)

	// ---------- wxapkg_search ----------
	s.AddTool(mcp.NewTool("wxapkg_search",
		mcp.WithDescription(
			"在指定目录中做正则搜索，是暴露面审计的核心工具。以当前用户权限读盘，不是沙箱。\n"+
				"常用 pattern：https?://[^\"']+ （接口域名）、(secret|token|apikey|aes|md5)[\"'\\s:=]+ 、"+
				"wx\\.[a-zA-Z]+ （用到的微信 API）。\n"+
				"默认跳过 .astc/.png/.jpg/.wasm 等二进制文件。"),
		mcp.WithString("dir", mcp.Description("要搜索的目录"), mcp.Required()),
		mcp.WithString("pattern", mcp.Description("正则表达式（Go regexp 语法）"), mcp.Required()),
		mcp.WithString("file_glob",
			mcp.Description("文件名过滤，如 *.js 或 game.js。留空表示所有文本文件。")),
		mcp.WithNumber("max_matches", mcp.Description("最多返回多少条命中，默认 50。")),
		mcp.WithNumber("context_chars", mcp.Description("每条命中前后各带多少字符上下文，默认 80。")),
		mcp.WithBoolean("count_only",
			mcp.Description("只按文件统计命中数量，不返回具体内容。默认 false。")),
	), handleSearch)
}

var binaryExts = map[string]bool{
	".astc": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".wasm": true, ".mp3": true, ".ogg": true, ".mp4": true, ".ttf": true,
	".woff": true, ".woff2": true, ".pkm": true, ".pvr": true, ".webp": true,
	".plist": false,
}

func isBinaryPath(p string) bool {
	return binaryExts[strings.ToLower(filepath.Ext(p))]
}

// ---------- list files ----------
func handleListFiles(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dir := reqStr(req, "dir")
	extFilter := req.GetString("ext", "")
	sortBySize := req.GetBool("sort_by_size", false)
	limit := int(req.GetFloat("limit", 200))

	if _, err := os.Stat(dir); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("目录不存在: %s", dir)), nil
	}

	var want map[string]bool
	if extFilter != "" {
		want = map[string]bool{}
		for _, e := range strings.Split(extFilter, ",") {
			e = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(e), "."))
			if e != "" {
				want["."+strings.ToLower(e)] = true
			}
		}
	}

	type entry struct {
		rel  string
		size int64
	}
	var list []entry
	var total int64
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		if want != nil && !want[ext] {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		list = append(list, entry{filepath.ToSlash(rel), info.Size()})
		total += info.Size()
		return nil
	})

	if sortBySize {
		sort.Slice(list, func(i, j int) bool { return list[i].size > list[j].size })
	} else {
		sort.Slice(list, func(i, j int) bool { return list[i].rel < list[j].rel })
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("目录: %s\n共 %d 个文件，合计 %s", dir, len(list), humanSize(total)))
	if len(list) > limit {
		sb.WriteString(fmt.Sprintf("（仅显示前 %d 条）", limit))
	}
	sb.WriteString("\n\n")
	for i, e := range list {
		if i >= limit {
			break
		}
		sb.WriteString(fmt.Sprintf("%10s  %s\n", humanSize(e.size), e.rel))
	}
	if len(list) == 0 {
		sb.WriteString("(无匹配文件)")
	}
	return mcp.NewToolResultText(sb.String()), nil
}

// ---------- read file ----------
func handleReadFile(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	p := reqStr(req, "path")
	maxBytes := int(req.GetFloat("max_bytes", 20000))
	offset := int(req.GetFloat("offset", 0))

	st, err := os.Stat(p)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("文件不存在或无法访问: %s (%v)", p, err)), nil
	}
	if st.IsDir() {
		return mcp.NewToolResultError("path 是目录，请改用 wxapkg_list_files"), nil
	}

	if isBinaryPath(p) {
		f, err := os.Open(p)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		defer f.Close()
		head := make([]byte, 64)
		n, _ := f.Read(head)
		return mcp.NewToolResultText(fmt.Sprintf(
			"二进制文件，无法作为文本显示。\n大小: %s\n头部 64 字节(hex): %x",
			humanSize(st.Size()), head[:n])), nil
	}

	raw, err := os.ReadFile(p)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	if offset > len(raw) {
		offset = len(raw)
	}
	chunk := raw[offset:]
	truncated := false
	if len(chunk) > maxBytes {
		chunk = chunk[:maxBytes]
		truncated = true
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("文件: %s\n大小: %s", p, humanSize(st.Size())))
	if offset > 0 {
		sb.WriteString(fmt.Sprintf("（从 %d 字节处开始）", offset))
	}
	sb.WriteString("\n\n")
	sb.WriteString(string(chunk))
	if truncated {
		sb.WriteString(fmt.Sprintf(
			"\n\n...[已截断，仅显示 %d/%d 字节。调大 max_bytes 或用 offset 分段读取]",
			maxBytes, len(raw)-offset))
	}
	return mcp.NewToolResultText(sb.String()), nil
}

// ---------- search ----------
type matchItem struct {
	file string
	line int
	text string
}

func handleSearch(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	dir := reqStr(req, "dir")
	pattern := reqStr(req, "pattern")
	glob := req.GetString("file_glob", "")
	maxMatches := int(req.GetFloat("max_matches", 50))
	ctxChars := int(req.GetFloat("context_chars", 80))
	countOnly := req.GetBool("count_only", false)

	re, err := regexp.Compile(pattern)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("正则表达式非法: %v", err)), nil
	}
	if _, err := os.Stat(dir); err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("目录不存在: %s", dir)), nil
	}

	fileCounts := map[string]int{}
	var matches []matchItem
	total := 0
	stopped := false

	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if stopped || err != nil || info.IsDir() {
			return nil
		}
		if isBinaryPath(p) {
			return nil
		}
		if glob != "" {
			if ok, _ := filepath.Match(glob, filepath.Base(p)); !ok {
				return nil
			}
		}
		// 跳过超大文件（通常是引擎 bundle）
		if info.Size() > 32*1024*1024 {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if !utf8Like(data) {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)

		for _, loc := range re.FindAllIndex(data, -1) {
			total++
			fileCounts[rel]++
			if countOnly || len(matches) >= maxMatches {
				continue
			}
			s, e := loc[0]-ctxChars, loc[1]+ctxChars
			if s < 0 {
				s = 0
			}
			if e > len(data) {
				e = len(data)
			}
			line := bytes.Count(data[:loc[0]], []byte("\n")) + 1
			snippet := strings.ReplaceAll(string(data[s:e]), "\r", "")
			snippet = strings.Join(strings.Split(snippet, "\n"), " ")
			snippet = collapseSpaces(snippet)
			matches = append(matches, matchItem{rel, line, snippet})
		}
		if !countOnly && len(matches) >= maxMatches {
			stopped = true
		}
		return nil
	})

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("搜索: /%s/  目录: %s\n命中总数: %d", pattern, dir, total))

	if countOnly {
		type fc struct {
			f string
			c int
		}
		var counts []fc
		for f, c := range fileCounts {
			counts = append(counts, fc{f, c})
		}
		sort.Slice(counts, func(i, j int) bool { return counts[i].c > counts[j].c })
		sb.WriteString("\n\n按文件统计（Top 30）：\n")
		for i, c := range counts {
			if i >= 30 {
				break
			}
			sb.WriteString(fmt.Sprintf("  %-60s %d\n", c.f, c.c))
		}
		return mcp.NewToolResultText(sb.String()), nil
	}

	sb.WriteString(fmt.Sprintf("，展示前 %d 条：\n\n", len(matches)))
	for i, m := range matches {
		sb.WriteString(fmt.Sprintf("[%d] %s:%d\n    %s\n\n", i+1, m.file, m.line, m.text))
	}
	if total > len(matches) {
		sb.WriteString(fmt.Sprintf("...还有 %d 条未展示。可调大 max_matches，或用更精确的 pattern / file_glob 收窄范围。\n",
			total-len(matches)))
	}
	if total == 0 {
		sb.WriteString("\n(无命中)")
	}
	_ = strconv.Itoa(0)
	return mcp.NewToolResultText(sb.String()), nil
}

// utf8Like 粗略判断是否为文本，避免把二进制当文本搜
func utf8Like(b []byte) bool {
	if len(b) == 0 {
		return true
	}
	n := len(b)
	if n > 4096 {
		n = 4096
	}
	bad := 0
	for _, c := range b[:n] {
		if c == 0 {
			bad++
			continue
		}
		if c < 0x09 || (c > 0x0d && c < 0x20) {
			bad++
		}
	}
	return bad*100/n < 5
}

func collapseSpaces(s string) string {
	var sb strings.Builder
	prevSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if prevSpace {
				continue
			}
			prevSpace = true
			sb.WriteRune(' ')
			continue
		}
		prevSpace = false
		sb.WriteRune(r)
	}
	return strings.TrimSpace(sb.String())
}
