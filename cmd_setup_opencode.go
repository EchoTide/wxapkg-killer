package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// opencodeClient OpenCode V2 把本地 MCP 写在 mcp.servers.<name>，
// command 是「可执行文件 + 参数」数组，不是 mcpServers.command/args。
func opencodeClient() clientSpec {
	dir := opencodeConfigDir()
	return clientSpec{
		ID:        "opencode",
		Name:      "OpenCode",
		Format:    "opencode",
		AbsConfig: opencodeConfigPath(),
		AbsSkill:  filepath.Join(dir, "skills"),
		NextStep:  "OpenCode：重开一个会话即可调用（当前会话不会热加载新 MCP）。",
	}
}

func opencodeConfigDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "opencode")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "opencode")
	}
	return filepath.Join(home, ".config", "opencode")
}

// opencodeConfigPath 优先已有的 opencode.json；只有 jsonc 时改 jsonc；都没有则创建 json。
func opencodeConfigPath() string {
	dir := opencodeConfigDir()
	jsonPath := filepath.Join(dir, "opencode.json")
	jsoncPath := filepath.Join(dir, "opencode.jsonc")
	if st, err := os.Stat(jsonPath); err == nil && !st.IsDir() {
		return jsonPath
	}
	if st, err := os.Stat(jsoncPath); err == nil && !st.IsDir() {
		return jsoncPath
	}
	return jsonPath
}

func opencodeEntryCommand(p string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	return commandFromOpenCode(b)
}

func commandFromOpenCode(src []byte) (string, bool) {
	var doc map[string]any
	if err := json.Unmarshal(normalizeJSONC(src), &doc); err != nil {
		return "", false
	}
	mcp, _ := doc["mcp"].(map[string]any)
	if mcp == nil {
		return "", false
	}
	servers, _ := mcp["servers"].(map[string]any)
	if servers == nil {
		return "", false
	}
	entry, _ := servers[serverEntryName].(map[string]any)
	if entry == nil {
		return "", false
	}
	switch c := entry["command"].(type) {
	case []any:
		if len(c) == 0 {
			return "", false
		}
		s, _ := c[0].(string)
		return s, s != ""
	case string:
		return c, c != ""
	default:
		return "", false
	}
}

func openCodeEntryJSON(exe string) string {
	eb, _ := json.Marshal(exe)
	return `{"type":"local","command":[` + string(eb) + `,"serve"]}`
}

func renderNewOpenCodeConfig(exe string) []byte {
	eb, _ := json.Marshal(exe)
	return []byte(fmt.Sprintf(`{
  "$schema": "https://opencode.ai/config.json",
  "mcp": {
    "servers": {
      "%s": {
        "type": "local",
        "command": [%s, "serve"]
      }
    }
  }
}
`, serverEntryName, string(eb)))
}

func applyOpenCodeClient(p, exe string, dry bool) error {
	b, err := os.ReadFile(p)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var out []byte
	changed := true
	if !existed || len(bytes.TrimSpace(b)) == 0 {
		out = renderNewOpenCodeConfig(exe)
	} else {
		out, changed, err = upsertOpenCodeServer(b, exe)
		if err != nil {
			return fmt.Errorf("无法合并 OpenCode 配置（未改动原文件）：%w", err)
		}
	}
	if dry || !changed {
		return nil
	}
	if existed && len(bytes.TrimSpace(b)) > 0 {
		if _, err := backupFile(p); err != nil {
			return fmt.Errorf("备份失败（未写入）: %w", err)
		}
	}
	return writeAtomic(p, out)
}

func removeOpenCodeEntry(p string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	out, changed, err := removeOpenCodeServer(b)
	if err != nil {
		return fmt.Errorf("无法从 OpenCode 配置移除条目（未改动原文件）：%w", err)
	}
	if !changed {
		return nil
	}
	return writeAtomic(p, out)
}

// upsertOpenCodeServer 只改 mcp.servers.wxapkg-killer，其余字节原样保留。
// 已指向同一二进制时不改文件。解析失败则返回错误，调用方不得写盘。
func upsertOpenCodeServer(src []byte, exe string) ([]byte, bool, error) {
	if cmd, ok := commandFromOpenCode(src); ok && samePath(cmd, exe) {
		return src, false, nil
	}
	root, err := rootObjectStart(src)
	if err != nil {
		return nil, false, err
	}
	entry := openCodeEntryJSON(exe)
	mcp, _, hasMCP, err := findMember(src, root, "mcp")
	if err != nil {
		return nil, false, err
	}
	if !hasMCP {
		out, err := insertMember(src, root, "mcp", fmt.Sprintf(`{"servers":{%q:%s}}`, serverEntryName, entry))
		return out, true, err
	}
	if mcp.valStart >= len(src) || src[mcp.valStart] != '{' {
		return nil, false, fmt.Errorf("mcp 不是对象，未做任何修改")
	}
	servers, _, hasServers, err := findMember(src, mcp.valStart, "servers")
	if err != nil {
		return nil, false, err
	}
	if !hasServers {
		out, err := insertMember(src, mcp.valStart, "servers", fmt.Sprintf(`{%q:%s}`, serverEntryName, entry))
		return out, true, err
	}
	if servers.valStart >= len(src) || src[servers.valStart] != '{' {
		return nil, false, fmt.Errorf("mcp.servers 不是对象，未做任何修改")
	}
	existing, _, hasEntry, err := findMember(src, servers.valStart, serverEntryName)
	if err != nil {
		return nil, false, err
	}
	if hasEntry {
		out := make([]byte, 0, len(src)+len(entry))
		out = append(out, src[:existing.valStart]...)
		out = append(out, entry...)
		out = append(out, src[existing.valEnd:]...)
		return out, true, nil
	}
	out, err := insertMember(src, servers.valStart, serverEntryName, entry)
	return out, true, err
}

func removeOpenCodeServer(src []byte) ([]byte, bool, error) {
	root, err := rootObjectStart(src)
	if err != nil {
		return nil, false, err
	}
	mcp, _, hasMCP, err := findMember(src, root, "mcp")
	if err != nil || !hasMCP || mcp.valStart >= len(src) || src[mcp.valStart] != '{' {
		return src, false, err
	}
	servers, _, hasServers, err := findMember(src, mcp.valStart, "servers")
	if err != nil || !hasServers || servers.valStart >= len(src) || src[servers.valStart] != '{' {
		return src, false, err
	}
	return removeMember(src, servers.valStart, serverEntryName)
}

// ---------------------------------------------------------------------------
// JSON / JSONC 扫描。只用于定位对象成员，不重排、不重写无关内容。
// ---------------------------------------------------------------------------

type jmember struct {
	key      string
	keyStart int
	valStart int
	valEnd   int
}

func rootObjectStart(b []byte) (int, error) {
	i := skipWS(b, 0)
	if i >= len(b) || b[i] != '{' {
		return 0, fmt.Errorf("配置根节点不是 JSON 对象")
	}
	return i, nil
}

func insertMember(src []byte, objOpen int, key, rawValue string) ([]byte, error) {
	if objOpen >= len(src) || src[objOpen] != '{' {
		return nil, fmt.Errorf("插入位置不是对象")
	}
	piece := []byte(fmt.Sprintf("%q:%s", key, rawValue))
	i := skipWS(src, objOpen+1)
	out := make([]byte, 0, len(src)+len(piece)+1)
	out = append(out, src[:objOpen+1]...)
	out = append(out, piece...)
	if i < len(src) && src[i] != '}' {
		out = append(out, ',')
	}
	out = append(out, src[objOpen+1:]...)
	return out, nil
}

func removeMember(src []byte, objOpen int, key string) ([]byte, bool, error) {
	m, _, found, err := findMember(src, objOpen, key)
	if err != nil || !found {
		return src, false, err
	}
	start := m.keyStart
	end := m.valEnd
	j := skipWS(src, end)
	if j < len(src) && src[j] == ',' {
		end = j + 1
	} else {
		k := m.keyStart - 1
		for k >= 0 {
			c := src[k]
			if c == ' ' || c == '\n' || c == '\t' || c == '\r' {
				k--
				continue
			}
			break
		}
		if k >= 0 && src[k] == ',' {
			start = k
		}
	}
	out := make([]byte, 0, len(src))
	out = append(out, src[:start]...)
	out = append(out, src[end:]...)
	return out, true, nil
}

func findMember(b []byte, objOpen int, want string) (jmember, int, bool, error) {
	if objOpen >= len(b) || b[objOpen] != '{' {
		return jmember{}, 0, false, fmt.Errorf("不是对象")
	}
	i := objOpen + 1
	var found jmember
	ok := false
	for {
		i = skipWS(b, i)
		if i >= len(b) {
			return jmember{}, 0, false, fmt.Errorf("对象未闭合")
		}
		if b[i] == '}' {
			return found, i, ok, nil
		}
		keyStart := i
		end, err := skipString(b, i)
		if err != nil {
			return jmember{}, 0, false, err
		}
		key, err := unquoteJSON(b[keyStart:end])
		if err != nil {
			return jmember{}, 0, false, err
		}
		i = skipWS(b, end)
		if i >= len(b) || b[i] != ':' {
			return jmember{}, 0, false, fmt.Errorf("期望冒号")
		}
		i++
		valStart := skipWS(b, i)
		valEnd, err := skipValue(b, valStart)
		if err != nil {
			return jmember{}, 0, false, err
		}
		if key == want {
			found = jmember{key: key, keyStart: keyStart, valStart: valStart, valEnd: valEnd}
			ok = true
		}
		i = skipWS(b, valEnd)
		if i < len(b) && b[i] == ',' {
			i++
		}
	}
}

func unquoteJSON(raw []byte) (string, error) {
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", err
	}
	return s, nil
}

func skipWS(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		case '/':
			if i+1 < len(b) && b[i+1] == '/' {
				i += 2
				for i < len(b) && b[i] != '\n' {
					i++
				}
				continue
			}
			if i+1 < len(b) && b[i+1] == '*' {
				i += 2
				for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
					i++
				}
				if i+1 < len(b) {
					i += 2
				} else {
					i = len(b)
				}
				continue
			}
			return i
		default:
			return i
		}
	}
	return i
}

func skipString(b []byte, i int) (int, error) {
	if i >= len(b) || b[i] != '"' {
		return i, fmt.Errorf("期望字符串")
	}
	i++
	for i < len(b) {
		if b[i] == '\\' {
			if i+1 >= len(b) {
				return i, fmt.Errorf("字符串转义不完整")
			}
			i += 2
			continue
		}
		if b[i] == '"' {
			return i + 1, nil
		}
		i++
	}
	return i, fmt.Errorf("字符串未闭合")
}

func skipLiteral(b []byte, i int, lit string) (int, error) {
	if i+len(lit) > len(b) || string(b[i:i+len(lit)]) != lit {
		return i, fmt.Errorf("期望 %s", lit)
	}
	return i + len(lit), nil
}

func skipNumber(b []byte, i int) int {
	if i < len(b) && (b[i] == '-' || b[i] == '+') {
		i++
	}
	for i < len(b) {
		c := b[i]
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-' {
			i++
			continue
		}
		break
	}
	return i
}

func skipValue(b []byte, i int) (int, error) {
	i = skipWS(b, i)
	if i >= len(b) {
		return i, fmt.Errorf("意外的文件结尾")
	}
	switch b[i] {
	case '"':
		return skipString(b, i)
	case '{', '[':
		return skipContainer(b, i)
	case 't':
		return skipLiteral(b, i, "true")
	case 'f':
		return skipLiteral(b, i, "false")
	case 'n':
		return skipLiteral(b, i, "null")
	default:
		if b[i] == '-' || b[i] == '+' || (b[i] >= '0' && b[i] <= '9') {
			return skipNumber(b, i), nil
		}
		return i, fmt.Errorf("无法识别的 JSON 记号")
	}
}

func skipContainer(b []byte, i int) (int, error) {
	open := b[i]
	closer := byte('}')
	if open == '[' {
		closer = ']'
	}
	i++
	for {
		i = skipWS(b, i)
		if i >= len(b) {
			return i, fmt.Errorf("容器未闭合")
		}
		if b[i] == closer {
			return i + 1, nil
		}
		if open == '{' {
			var err error
			i, err = skipString(b, i)
			if err != nil {
				return i, err
			}
			i = skipWS(b, i)
			if i >= len(b) || b[i] != ':' {
				return i, fmt.Errorf("期望冒号")
			}
			i++
		}
		var err error
		i, err = skipValue(b, i)
		if err != nil {
			return i, err
		}
		i = skipWS(b, i)
		if i < len(b) && b[i] == ',' {
			i++
		}
	}
}

// normalizeJSONC 去掉注释和尾随逗号，仅用于解析，不用于写回。
func normalizeJSONC(src []byte) []byte {
	if bytes.HasPrefix(src, []byte{0xEF, 0xBB, 0xBF}) {
		src = src[3:]
	}
	out := make([]byte, 0, len(src))
	inStr := false
	esc := false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inStr {
			out = append(out, c)
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '/' {
			i += 2
			for i < len(src) && src[i] != '\n' {
				i++
			}
			if i < len(src) && src[i] == '\n' {
				out = append(out, '\n')
			}
			continue
		}
		if c == '/' && i+1 < len(src) && src[i+1] == '*' {
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			if i+1 < len(src) {
				i++
			}
			continue
		}
		if c == ',' {
			j := skipWS(src, i+1)
			if j < len(src) && (src[j] == '}' || src[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}
