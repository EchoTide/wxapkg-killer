package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpsertTOMLSectionKeepsOtherSections(t *testing.T) {
	src := []byte("model = \"gpt\"\n\n[mcp_servers.other]\ncommand = \"echo\"\n")
	out := upsertTOMLSection(src, "[mcp_servers.wxapkg-killer]", []string{
		`command = "C:\\bin\\wxapkg-killer.exe"`,
		`args = ["serve"]`,
	})
	text := string(out)
	if !strings.Contains(text, `model = "gpt"`) {
		t.Fatalf("丢失无关内容:\n%s", text)
	}
	if !strings.Contains(text, "[mcp_servers.other]") || !strings.Contains(text, `command = "echo"`) {
		t.Fatalf("丢失其它 section:\n%s", text)
	}
	if strings.Count(text, "[mcp_servers.wxapkg-killer]") != 1 {
		t.Fatalf("section 数量不对:\n%s", text)
	}

	again := upsertTOMLSection(out, "[mcp_servers.wxapkg-killer]", []string{
		`command = "D:\\new.exe"`,
		`args = ["serve"]`,
	})
	if strings.Count(string(again), "[mcp_servers.wxapkg-killer]") != 1 {
		t.Fatalf("重复 upsert 产生了多个 section:\n%s", again)
	}
	if !strings.Contains(string(again), `command = "D:\\new.exe"`) {
		t.Fatalf("未替换旧 command:\n%s", again)
	}
	if strings.Contains(string(again), `C:\\bin\\wxapkg-killer.exe`) {
		t.Fatalf("旧 command 仍在:\n%s", again)
	}
}

func TestApplyJSONClientPreservesUnrelatedKeys(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mcp.json")
	orig := []byte("{\n  \"theme\": \"dark\",\n  \"mcpServers\": {\n    \"other\": { \"command\": \"echo\" }\n  }\n}\n")
	if err := os.WriteFile(p, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	exe := `C:\tools\wxapkg-killer.exe`
	if err := applyJSONClient(p, exe, false); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("写出的不是合法 JSON: %v\n%s", err, b)
	}
	if doc["theme"] != "dark" {
		t.Fatalf("无关字段被改掉: %#v", doc["theme"])
	}
	servers := doc["mcpServers"].(map[string]any)
	if _, ok := servers["other"]; !ok {
		t.Fatal("其它 MCP 被删掉了")
	}
	entry := servers["wxapkg-killer"].(map[string]any)
	if entry["command"] != exe {
		t.Fatalf("command = %#v", entry["command"])
	}
	bk, err := os.ReadFile(p + backupSuffix)
	if err != nil {
		t.Fatal(err)
	}
	if string(bk) != string(orig) {
		t.Fatalf("备份不是首次内容:\n%s", bk)
	}
	if err := os.WriteFile(p, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := backupFile(p); err != nil {
		t.Fatal(err)
	}
	bk2, _ := os.ReadFile(p + backupSuffix)
	if string(bk2) != string(orig) {
		t.Fatal("第二次备份覆盖了首次快照")
	}
}

func TestUpsertOpenCodePreservesSiblingsAndJSONC(t *testing.T) {
	src := []byte(`{
  // keep me
  "mcp": {
    "chrome-devtools": { "type": "local", "command": ["npx", "-y", "chrome-devtools-mcp"], },
    "servers": {
      "other": { "type": "local", "command": ["echo", "hi"], },
    },
  },
  "provider": { "name": "keep-me", "note": "https://example.com//not-a-comment", },
}
`)
	exe := `C:\tools\wxapkg-killer.exe`
	out, changed, err := upsertOpenCodeServer(src, exe)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("首次写入应有变更")
	}
	if !bytesContainsAll(out, `"chrome-devtools"`, `"other"`, `"keep-me"`, `https://example.com//not-a-comment`, `// keep me`) {
		t.Fatalf("无关内容被改写:\n%s", out)
	}
	cmd, ok := commandFromOpenCode(out)
	if !ok || !samePath(cmd, exe) {
		t.Fatalf("写完读不到 command: %q ok=%v\n%s", cmd, ok, out)
	}

	again, changed, err := upsertOpenCodeServer(out, exe)
	if err != nil {
		t.Fatal(err)
	}
	if changed || string(again) != string(out) {
		t.Fatal("同一二进制不应再次改写")
	}

	replaced, changed, err := upsertOpenCodeServer(out, `D:\new\wxapkg-killer.exe`)
	if err != nil || !changed {
		t.Fatalf("更换路径应改写: changed=%v err=%v", changed, err)
	}
	cmd, ok = commandFromOpenCode(replaced)
	if !ok || !samePath(cmd, `D:\new\wxapkg-killer.exe`) {
		t.Fatalf("替换后 command=%q", cmd)
	}
	if !bytesContainsAll(replaced, `"chrome-devtools"`, `"provider"`) {
		t.Fatalf("替换时丢了其它字段:\n%s", replaced)
	}
}

func TestUpsertOpenCodeCreatesServersObject(t *testing.T) {
	src := []byte("{\n  \"mcp\": {\n    \"chrome-devtools\": { \"command\": \"npx\" }\n  }\n}\n")
	out, changed, err := upsertOpenCodeServer(src, `C:\bin\wxapkg-killer.exe`)
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	if !bytesContainsAll(out, `"chrome-devtools"`, `"servers"`, `"wxapkg-killer"`) {
		t.Fatalf("未插入 servers:\n%s", out)
	}
	cmd, ok := commandFromOpenCode(out)
	if !ok || !samePath(cmd, `C:\bin\wxapkg-killer.exe`) {
		t.Fatalf("command=%q ok=%v\n%s", cmd, ok, out)
	}
}

func TestRemoveOpenCodeKeepsOtherServers(t *testing.T) {
	src := []byte(`{"mcp":{"servers":{"other":{"command":"echo"},"wxapkg-killer":{"type":"local","command":["a.exe","serve"]}},"chrome-devtools":{"command":"npx"}}}`)
	out, changed, err := removeOpenCodeServer(src)
	if err != nil || !changed {
		t.Fatal(err, changed)
	}
	if bytes.Contains(out, []byte(`"wxapkg-killer"`)) {
		t.Fatalf("条目还在:\n%s", out)
	}
	if !bytesContainsAll(out, `"other"`, `"chrome-devtools"`) {
		t.Fatalf("误删其它字段:\n%s", out)
	}
	var doc map[string]any
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("移除后不是合法 JSON: %v\n%s", err, out)
	}
}

func TestApplyOpenCodeClientDryRun(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "opencode.json")
	orig := []byte("{ \"provider\": { \"name\": \"x\" } }\n")
	if err := os.WriteFile(p, orig, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := applyOpenCodeClient(p, `C:\bin\wxapkg-killer.exe`, true); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if string(b) != string(orig) {
		t.Fatalf("dry-run 改了文件:\n%s", b)
	}
	if _, err := os.Stat(p + backupSuffix); !os.IsNotExist(err) {
		t.Fatal("dry-run 不应生成备份")
	}
}

func TestNormalizeJSONCKeepsStringSlashes(t *testing.T) {
	src := []byte("{ \"u\": \"https://example.com//x\", /* c */ \"n\": 1, }\n")
	var doc map[string]any
	if err := json.Unmarshal(normalizeJSONC(src), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["u"] != "https://example.com//x" || doc["n"] != float64(1) {
		t.Fatalf("%#v", doc)
	}
}

func bytesContainsAll(b []byte, parts ...string) bool {
	s := string(b)
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}
