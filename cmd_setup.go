package main

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// 随二进制分发的 Agent 工作流指令，setup 时写入各客户端的 skills 目录
//
//go:embed skills/wxapkg-unpack/SKILL.md
var skillMarkdown []byte

const (
	serverEntryName = "wxapkg-killer"
	skillDirName    = "wxapkg-unpack"
	backupSuffix    = ".wxapkg-killer.bak"
)

// 所有 JSON 客户端里存放 MCP server 的键（目前均为顶层 mcpServers）
var jsonKeyServers = []string{"mcpServers"}

// ---------------------------------------------------------------------------
// 客户端注册表
// ---------------------------------------------------------------------------

type clientSpec struct {
	ID        string   // --client 使用的标识
	Name      string   // 展示名
	Format    string   // "json" | "toml" | "opencode"
	RelPath   []string // 配置文件相对 HOME 的路径
	JSONKey   []string // JSON 中 mcpServers 的键路径（Format=json 时有效）
	SkillRel  []string // 技能目录相对 HOME 的路径，为空表示不支持
	AbsConfig string   // 非空时忽略 RelPath（OpenCode 可能受 XDG_CONFIG_HOME 影响）
	AbsSkill  string   // 非空时忽略 SkillRel
	NextStep  string   // 注册完成后的额外步骤（信任门控 / 首次确认等），为空则只需重启
}

func (c clientSpec) configPath(home string) string {
	if c.AbsConfig != "" {
		return c.AbsConfig
	}
	return filepath.Join(append([]string{home}, c.RelPath...)...)
}

func (c clientSpec) skillDir(home string) string {
	if c.AbsSkill != "" {
		return c.AbsSkill
	}
	if len(c.SkillRel) == 0 {
		return ""
	}
	return filepath.Join(append([]string{home}, c.SkillRel...)...)
}

// claudeDesktopClient Claude Desktop 的配置路径各平台不同
func claudeDesktopClient() clientSpec {
	var rel []string
	switch runtime.GOOS {
	case "darwin":
		rel = []string{"Library", "Application Support", "Claude", "claude_desktop_config.json"}
	case "windows":
		rel = []string{"AppData", "Roaming", "Claude", "claude_desktop_config.json"}
	default:
		rel = []string{".config", "Claude", "claude_desktop_config.json"}
	}
	return clientSpec{
		ID: "claude_desktop", Name: "Claude Desktop", Format: "json",
		RelPath: rel, JSONKey: []string{"mcpServers"},
		NextStep: "Claude Desktop：重启应用；首次调用工具时需在客户端内确认信任。",
	}
}

func defaultClients() []clientSpec {
	cs := []clientSpec{
		{ID: "claude_code", Name: "Claude Code", Format: "json",
			RelPath: []string{".claude.json"}, JSONKey: []string{"mcpServers"},
			SkillRel: []string{".claude", "skills"},
			NextStep: "Claude Code：重启后首次调用本 MCP 工具时会弹出信任确认，放行即可。"},
		{ID: "codex", Name: "Codex CLI", Format: "toml",
			RelPath:  []string{".codex", "config.toml"},
			SkillRel: []string{".codex", "skills"}},
		{ID: "cursor", Name: "Cursor", Format: "json",
			RelPath: []string{".cursor", "mcp.json"}, JSONKey: []string{"mcpServers"},
			SkillRel: []string{".cursor", "skills"}},
		{ID: "workbuddy", Name: "WorkBuddy", Format: "json",
			RelPath: []string{".workbuddy", "mcp.json"}, JSONKey: []string{"mcpServers"},
			SkillRel: []string{".workbuddy", "skills"},
			NextStep: "WorkBuddy：写盘不会自动生效。请打开左侧「专家·技能·连接器」→ 连接器 → 右上角「自定义连接器」，" +
				"在 MCP 服务管理里找到 wxapkg-killer，点击「信任」并启用；然后新建一个会话即可调用（旧会话不会热加载）。"},
		{ID: "gemini", Name: "Gemini CLI", Format: "json",
			RelPath: []string{".gemini", "settings.json"}, JSONKey: []string{"mcpServers"},
			SkillRel: []string{".agents", "skills"}},
		{ID: "windsurf", Name: "Windsurf", Format: "json",
			RelPath: []string{".codeium", "windsurf", "mcp_config.json"}, JSONKey: []string{"mcpServers"},
			SkillRel: []string{".agents", "skills"}},
		{ID: "continue", Name: "Continue", Format: "json",
			RelPath: []string{".continue", "config.json"}, JSONKey: []string{"mcpServers"},
			SkillRel: []string{".agents", "skills"}},
	}
	return append(cs, claudeDesktopClient(), opencodeClient())
}

// ---------------------------------------------------------------------------
// 探测
// ---------------------------------------------------------------------------

type detectedClient struct {
	Spec      clientSpec
	Config    string // 配置文件绝对路径
	Installed bool   // 客户端目录存在
	HasFile   bool   // 配置文件已存在
	HasEntry  bool   // 已注册过本 MCP
	EntryOK   bool   // 已注册且命令指向当前二进制
}

func parentDirExists(p string) bool {
	st, err := os.Stat(filepath.Dir(p))
	return err == nil && st.IsDir()
}

func detectClients(home, exe string) []detectedClient {
	var out []detectedClient
	for _, c := range defaultClients() {
		p := c.configPath(home)
		st, err := os.Stat(p)
		hasFile := err == nil && !st.IsDir()
		installed := hasFile || parentDirExists(p)
		d := detectedClient{Spec: c, Config: p, Installed: installed, HasFile: hasFile}
		if hasFile {
			switch c.Format {
			case "toml":
				d.HasEntry = tomlHasSection(p, serverEntryName)
				d.EntryOK = d.HasEntry && tomlSectionHasCommand(p, exe)
			case "opencode":
				cmd, ok := opencodeEntryCommand(p)
				d.HasEntry = ok
				d.EntryOK = ok && samePath(cmd, exe)
			default:
				cmd, ok := jsonEntryCommand(p, c.JSONKey)
				d.HasEntry = ok
				d.EntryOK = ok && samePath(cmd, exe)
			}
		}
		out = append(out, d)
	}
	return out
}

func samePath(a, b string) bool {
	if a == b {
		return true
	}
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

// ---------------------------------------------------------------------------
// 写入计划与执行
// ---------------------------------------------------------------------------

type planItem struct {
	ClientID   string `json:"client_id"`
	ClientName string `json:"client_name"`
	Config     string `json:"config"`
	Format     string `json:"format"`
	Action     string `json:"action"` // create / update / unchanged
	Skill      string `json:"skill,omitempty"`
}

func buildPlan(items []detectedClient, home string, withSkill bool) []planItem {
	var plan []planItem
	for _, d := range items {
		pi := planItem{
			ClientID:   d.Spec.ID,
			ClientName: d.Spec.Name,
			Config:     d.Config,
			Format:     d.Spec.Format,
			Action:     "create",
		}
		switch {
		case d.EntryOK:
			pi.Action = "unchanged"
		case d.HasEntry:
			pi.Action = "update"
		case d.HasFile:
			pi.Action = "update"
		}
		if withSkill {
			if sd := d.Spec.skillDir(home); sd != "" {
				pi.Skill = filepath.Join(sd, skillDirName, "SKILL.md")
			}
		}
		plan = append(plan, pi)
	}
	return plan
}

func applyPlan(plan []planItem, exe string, withSkill, dry bool) ([]planItem, error) {
	var applied []planItem
	for _, pi := range plan {
		if pi.Action == "unchanged" {
			// 技能文件仍要补齐
			if withSkill && pi.Skill != "" {
				if err := writeSkill(pi.Skill, dry); err != nil {
					return applied, fmt.Errorf("%s: 写入技能失败: %w", pi.ClientName, err)
				}
			}
			applied = append(applied, pi)
			continue
		}
		switch pi.Format {
		case "toml":
			if err := applyTOMLClient(pi.Config, exe, dry); err != nil {
				return applied, fmt.Errorf("%s: %w", pi.ClientName, err)
			}
		case "opencode":
			if err := applyOpenCodeClient(pi.Config, exe, dry); err != nil {
				return applied, fmt.Errorf("%s: %w", pi.ClientName, err)
			}
		default:
			if err := applyJSONClient(pi.Config, exe, dry); err != nil {
				return applied, fmt.Errorf("%s: %w", pi.ClientName, err)
			}
		}
		if withSkill && pi.Skill != "" {
			if err := writeSkill(pi.Skill, dry); err != nil {
				return applied, fmt.Errorf("%s: 写入技能失败: %w", pi.ClientName, err)
			}
		}
		applied = append(applied, pi)
	}
	return applied, nil
}

func writeSkill(target string, dry bool) error {
	if dry {
		return nil
	}
	return writeAtomic(target, skillMarkdown)
}

// ---------------------------------------------------------------------------
// JSON 配置合并
// ---------------------------------------------------------------------------

func applyJSONClient(p, exe string, dry bool) error {
	var doc map[string]any
	existed := false
	if b, err := os.ReadFile(p); err == nil && len(bytes.TrimSpace(b)) > 0 {
		existed = true
		if err := json.Unmarshal(b, &doc); err != nil {
			return fmt.Errorf("已有配置不是合法 JSON，未做任何修改：%w", err)
		}
	}
	if doc == nil {
		doc = map[string]any{}
	}

	// 逐层下钻到 mcpServers 所在容器（当前所有客户端均为顶层键）
	cur := doc
	for i := 0; i < len(jsonKeyServers)-1; i++ {
		next, ok := cur[jsonKeyServers[i]].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[jsonKeyServers[i]] = next
		}
		cur = next
	}
	last := jsonKeyServers[len(jsonKeyServers)-1]
	node := cur[last]
	m, _ := node.(map[string]any)
	if m == nil {
		if node != nil {
			return fmt.Errorf("已有配置中的 mcpServers 不是对象，未做任何修改")
		}
		m = map[string]any{}
	}
	m[serverEntryName] = map[string]any{
		"command": exe,
		"args":    []any{"serve"},
	}
	cur[last] = m

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	if dry {
		return nil
	}
	if existed {
		if _, err := backupFile(p); err != nil {
			return fmt.Errorf("备份失败（未写入）: %w", err)
		}
	}
	return writeAtomic(p, out)
}

func jsonEntryCommand(p string, keyPath []string) (string, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", false
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return "", false
	}
	node, ok := doc[keyPath[0]].(map[string]any)
	if !ok {
		return "", false
	}
	e, ok := node[serverEntryName].(map[string]any)
	if !ok {
		return "", false
	}
	c, _ := e["command"].(string)
	return c, c != ""
}

// ---------------------------------------------------------------------------
// TOML 配置（Codex）
// ---------------------------------------------------------------------------

func applyTOMLClient(p, exe string, dry bool) error {
	header := "[mcp_servers." + serverEntryName + "]"
	body := []string{
		"command = " + tomlQuote(exe),
		`args = ["serve"]`,
	}
	existing, _ := os.ReadFile(p)
	merged := upsertTOMLSection(existing, header, body)
	if dry {
		return nil
	}
	if len(bytes.TrimSpace(existing)) > 0 {
		if _, err := backupFile(p); err != nil {
			return fmt.Errorf("备份失败（未写入）: %w", err)
		}
	}
	return writeAtomic(p, merged)
}

// upsertTOMLSection 替换同名 section（不存在则追加到末尾）。
// 只按「行首为 [ 且行尾为 ]」识别 section 边界，其余内容原样保留。
func upsertTOMLSection(src []byte, header string, body []string) []byte {
	lines := strings.Split(string(src), "\n")
	out := make([]string, 0, len(lines)+len(body)+2)
	inSection := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			inSection = t == header
			if inSection {
				continue
			}
		}
		if inSection {
			continue
		}
		out = append(out, ln)
	}
	s := strings.TrimRight(strings.Join(out, "\n"), "\n")
	if s != "" {
		s += "\n\n"
	}
	return []byte(s + header + "\n" + strings.Join(body, "\n") + "\n")
}

func tomlQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func tomlHasSection(p, name string) bool {
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	return bytes.Contains(b, []byte("[mcp_servers."+name+"]"))
}

func tomlSectionHasCommand(p, exe string) bool {
	b, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	lines := strings.Split(string(b), "\n")
	header := "[mcp_servers." + serverEntryName + "]"
	in := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			in = t == header
			continue
		}
		if !in {
			continue
		}
		if strings.HasPrefix(t, "command") {
			idx := strings.Index(t, "=")
			if idx < 0 {
				continue
			}
			v := strings.TrimSpace(t[idx+1:])
			v = strings.Trim(v, `"`)
			return samePath(v, exe)
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// 文件工具
// ---------------------------------------------------------------------------

func writeAtomic(p string, data []byte) error {
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".wxapkg-killer-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	os.Chmod(name, 0o644)
	os.Remove(p) // Windows 下 os.Rename 无法覆盖已存在文件
	if err := os.Rename(name, p); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// backupFile 备份到 <path>.wxapkg-killer.bak，保留首次快照不覆盖
func backupFile(p string) (string, error) {
	bk := p + backupSuffix
	if _, err := os.Stat(bk); err == nil {
		return bk, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(bk, b, 0o644); err != nil {
		return "", err
	}
	return bk, nil
}

// ---------------------------------------------------------------------------
// CLI
// ---------------------------------------------------------------------------

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func currentExe() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if rp, err := filepath.EvalSymlinks(p); err == nil {
		p = rp
	}
	return filepath.Abs(p)
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

func findSpec(id string) (clientSpec, bool) {
	for _, c := range defaultClients() {
		if c.ID == id {
			return c, true
		}
	}
	return clientSpec{}, false
}

// selectTargets 决定本次要配置哪些客户端
func selectTargets(detected []detectedClient, wanted []string, all bool, home string) ([]detectedClient, error) {
	if len(wanted) > 0 {
		var out []detectedClient
		for _, w := range wanted {
			spec, ok := findSpec(w)
			if !ok {
				return nil, fmt.Errorf("未知客户端 %q，可用：%s", w, strings.Join(supportedIDs(), ", "))
			}
			out = append(out, detectedClient{Spec: spec, Config: spec.configPath(home), Installed: true})
		}
		return out, nil
	}
	if all {
		var out []detectedClient
		for _, d := range detected {
			if d.Installed {
				out = append(out, d)
			}
		}
		return out, nil
	}
	if !isTerminal() {
		return nil, fmt.Errorf("非交互环境需要显式指定 --client <id> 或 --all-detected")
	}
	return promptSelect(detected)
}

func supportedIDs() []string {
	var ids []string
	for _, c := range defaultClients() {
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)
	return ids
}

func promptSelect(detected []detectedClient) ([]detectedClient, error) {
	var candidates []detectedClient
	fmt.Fprintln(os.Stderr, "\n检测到以下 Agent（已注册的默认选中）：")
	for _, d := range detected {
		if !d.Installed {
			continue
		}
		candidates = append(candidates, d)
		mark := " "
		if d.HasEntry {
			mark = "*"
		}
		fmt.Fprintf(os.Stderr, "  [%s] %-2d %-16s %s\n", mark, len(candidates), d.Spec.ID, d.Spec.Name)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("未检测到任何支持的 Agent，可用 --client <id> 手动指定（支持：%s）", strings.Join(supportedIDs(), ", "))
	}
	fmt.Fprintf(os.Stderr, "\n输入序号选择（逗号分隔，已注册项默认包含；直接回车=仅已注册项）：")
	sc := bufio.NewScanner(os.Stdin)
	var out []detectedClient
	if sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			for _, d := range candidates {
				if d.HasEntry {
					out = append(out, d)
				}
			}
			return out, nil
		}
		for _, part := range strings.Split(line, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			n, err := strconv.Atoi(part)
			if err != nil || n < 1 || n > len(candidates) {
				fmt.Fprintf(os.Stderr, "忽略无效序号: %s\n", part)
				continue
			}
			out = append(out, candidates[n-1])
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("未选择任何 Agent")
	}
	return out, nil
}

func confirm(question string) bool {
	if !isTerminal() {
		return false
	}
	fmt.Fprintf(os.Stderr, "%s [y/N]: ", question)
	sc := bufio.NewScanner(os.Stdin)
	if !sc.Scan() {
		return false
	}
	a := strings.TrimSpace(strings.ToLower(sc.Text()))
	return a == "y" || a == "yes"
}

// ---------------------------------------------------------------------------
// setup
// ---------------------------------------------------------------------------

type setupResult struct {
	Status    string     `json:"status"` // applied | planned | cancelled | needs_confirmation
	Version   string     `json:"version"`
	Exe       string     `json:"exe"`
	Clients   []planItem `json:"clients"`
	Skill     bool       `json:"skill"`
	NextSteps []string   `json:"next_steps,omitempty"`
	Message   string     `json:"message,omitempty"`
}

func runSetup(args []string) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var wanted stringList
	fs.Var(&wanted, "client", "指定客户端 id，可重复，如 --client codex --client cursor")
	allDetected := fs.Bool("all-detected", false, "配置所有探测到的客户端")
	dry := fs.Bool("dry-run", false, "只输出计划，不写任何文件")
	jsonOut := fs.Bool("json", false, "以 JSON 输出结果")
	yes := fs.Bool("yes", false, "免确认（需配合 --client 或 --all-detected）")
	withSkill := fs.Bool("skill", true, "同时安装 Agent 工作流指令（SKILL.md）")
	extra := fs.String("config", "", "额外写入的 JSON 配置文件（需含 mcpServers 结构）")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	exe, err := currentExe()
	if err != nil {
		return fail(err.Error(), *jsonOut)
	}
	home := homeDir()

	detected := detectClients(home, exe)
	targets, err := selectTargets(detected, wanted, *allDetected, home)
	if err != nil {
		return fail(err.Error(), *jsonOut)
	}

	plan := buildPlan(targets, home, *withSkill)
	if *extra != "" {
		plan = append(plan, planItem{
			ClientID: "custom", ClientName: "自定义配置", Config: *extra,
			Format: "json", Action: "update",
		})
	}

	// 展示计划
	if !*jsonOut {
		fmt.Fprintf(os.Stderr, "\nwxapkg-killer %s  ·  setup\n", version)
		fmt.Fprintf(os.Stderr, "二进制: %s\n\n", exe)
		for _, pi := range plan {
			fmt.Fprintf(os.Stderr, "  %-16s %-8s %s\n", pi.ClientName, pi.Action, pi.Config)
			if pi.Skill != "" {
				fmt.Fprintf(os.Stderr, "  %-16s %-8s %s\n", "", "skill", pi.Skill)
			}
		}
		fmt.Fprintf(os.Stderr, "\n写入前会为每个已有配置生成一次备份（%s），且不改动其中无关内容。\n", backupSuffix)
	}

	if *dry {
		res := setupResult{Status: "planned", Version: version, Exe: exe, Clients: plan, Skill: *withSkill}
		return emit(res, *jsonOut, "预览完成，未写入任何文件。去掉 --dry-run 以应用。")
	}

	if !*yes {
		if !isTerminal() {
			res := setupResult{Status: "needs_confirmation", Version: version, Exe: exe, Clients: plan, Skill: *withSkill,
				Message: "非交互环境需加 --yes"}
			emit(res, *jsonOut, "")
			return 1
		}
		if !confirm("应用以上更改？") {
			res := setupResult{Status: "cancelled", Version: version, Exe: exe, Clients: plan, Skill: *withSkill}
			return emit(res, *jsonOut, "已取消，未做任何修改。")
		}
	}

	applied, err := applyPlan(plan, exe, *withSkill, false)
	if err != nil {
		return fail(err.Error(), *jsonOut)
	}
	steps := collectNextSteps(applied)
	res := setupResult{Status: "applied", Version: version, Exe: exe, Clients: applied, Skill: *withSkill, NextSteps: steps}
	msg := "已写入。"
	if len(steps) > 0 {
		msg += "\n\n后续步骤：\n  - " + strings.Join(steps, "\n  - ")
	} else {
		msg += "请重启对应 Agent 使其加载本 MCP。"
	}
	emit(res, *jsonOut, msg)
	return 0
}

// collectNextSteps 汇总已写入客户端还需要的额外步骤（信任门控 / 重启等），去重且保持顺序
func collectNextSteps(applied []planItem) []string {
	var out []string
	seen := map[string]bool{}
	for _, pi := range applied {
		for _, c := range defaultClients() {
			if c.ID != pi.ClientID {
				continue
			}
			hint := c.NextStep
			if hint == "" {
				hint = c.Name + "：重启即可加载。"
			}
			if !seen[hint] {
				seen[hint] = true
				out = append(out, hint)
			}
			break
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// doctor
// ---------------------------------------------------------------------------

type doctorSkill struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Fresh  bool   `json:"fresh"`
}

type doctorClient struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Config     string `json:"config"`
	Installed  bool   `json:"installed"`
	Registered bool   `json:"registered"`
	PointsHere bool   `json:"points_to_current_exe"`
}

type doctorReport struct {
	Version  string         `json:"version"`
	Exe      string         `json:"exe"`
	Healthy  bool           `json:"healthy"`
	Clients  []doctorClient `json:"clients"`
	Skills   []doctorSkill  `json:"skills"`
	Warnings []string       `json:"warnings,omitempty"`
}

func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOut := fs.Bool("json", false, "以 JSON 输出")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	exe, _ := currentExe()
	home := homeDir()
	detected := detectClients(home, exe)

	rep := doctorReport{Version: version, Exe: exe, Healthy: true}
	seenSkill := map[string]bool{}
	for _, d := range detected {
		rep.Clients = append(rep.Clients, doctorClient{
			ID: d.Spec.ID, Name: d.Spec.Name, Config: d.Config,
			Installed: d.Installed, Registered: d.HasEntry, PointsHere: d.EntryOK,
		})
		sd := d.Spec.skillDir(home)
		// 只为「已注册」的客户端检查技能：
		// 没注册的 Agent 本来就不该有技能文件，否则会误报成缺失
		if sd == "" || !d.HasEntry || seenSkill[sd] {
			continue
		}
		seenSkill[sd] = true
		sp := filepath.Join(sd, skillDirName, "SKILL.md")
		b, err := os.ReadFile(sp)
		ds := doctorSkill{Path: sp, Exists: err == nil}
		if err == nil {
			ds.Fresh = bytes.Equal(bytes.TrimSpace(b), bytes.TrimSpace(skillMarkdown))
		}
		rep.Skills = append(rep.Skills, ds)
	}
	for _, c := range rep.Clients {
		if c.Installed && c.Registered && !c.PointsHere {
			rep.Healthy = false
		}
	}
	for _, s := range rep.Skills {
		if !s.Fresh {
			rep.Healthy = false
		}
	}
	// WorkBuddy 信任门控：注册写入不等于生效，必须用户在连接器管理里点「信任」。
	// 审批记录为空时 Agent 不会启动本 MCP，doctor 需要明确提示，
	// 否则用户会以为装坏了（这是实际发生过的事故）。
	for _, d := range detected {
		if d.Spec.ID != "workbuddy" || !d.HasEntry {
			continue
		}
		ap := filepath.Join(filepath.Dir(d.Config), "mcp-approvals.json")
		if approvalsPending(ap) {
			rep.Warnings = append(rep.Warnings, d.Spec.NextStep)
		}
	}

	if *jsonOut {
		b, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(b))
		return 0
	}

	fmt.Printf("\nwxapkg-killer %s  ·  doctor\n二进制: %s\n\n", rep.Version, rep.Exe)
	fmt.Println("Agent 注册状态：")
	for _, c := range rep.Clients {
		if !c.Installed {
			fmt.Printf("  -      %-16s 未检测到\n", c.Name)
			continue
		}
		st := "未注册"
		if c.Registered && c.PointsHere {
			st = "已注册 ✓"
		} else if c.Registered {
			st = "已注册，但指向旧路径 ⚠"
		}
		fmt.Printf("  [%s] %-16s %s\n", map[bool]string{true: "*", false: " "}[c.Installed], c.Name, st)
	}
	if len(rep.Skills) > 0 {
		fmt.Println("\n工作流指令：")
		for _, s := range rep.Skills {
			st := "缺失"
			if s.Exists && s.Fresh {
				st = "已安装 ✓"
			} else if s.Exists {
				st = "已过期，运行 setup 更新"
			}
			fmt.Printf("  %-8s %s\n", st, s.Path)
		}
	}
	if len(rep.Warnings) > 0 {
		fmt.Println("待处理：")
		for _, w := range rep.Warnings {
			fmt.Printf("  ! %s\n", w)
		}
		fmt.Println()
	}
	fmt.Println()
	if rep.Healthy {
		fmt.Println("结论：健康。若仍无法调用工具，请确认 Agent 已重启。")
	} else {
		fmt.Println("结论：存在问题，运行 wxapkg-killer setup 修复。")
	}
	return 0
}

// approvalsPending 判断 Agent 的 MCP 审批记录文件是否处于「没有任何审批」状态。
// 文件缺失、无法解析或条目为空都返回 true——此时即使用户已写好配置，
// Agent 也不会启动对应的 MCP Server（工具列表里看不到它）。
func approvalsPending(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return true
	}
	return len(m) == 0
}

// ---------------------------------------------------------------------------
// uninstall
// ---------------------------------------------------------------------------

func runUninstall(args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var wanted stringList
	fs.Var(&wanted, "client", "指定客户端 id，可重复；不指定则清理所有已注册项")
	jsonOut := fs.Bool("json", false, "以 JSON 输出")
	yes := fs.Bool("yes", false, "免确认")
	removeSkill := fs.Bool("skill", true, "同时移除工作流指令")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	exe, _ := currentExe()
	home := homeDir()
	detected := detectClients(home, exe)

	var targets []detectedClient
	if len(wanted) > 0 {
		for _, w := range wanted {
			spec, ok := findSpec(w)
			if !ok {
				return fail(fmt.Sprintf("未知客户端 %q", w), *jsonOut)
			}
			for _, d := range detected {
				if d.Spec.ID == spec.ID {
					targets = append(targets, d)
				}
			}
		}
	} else {
		for _, d := range detected {
			if d.HasEntry {
				targets = append(targets, d)
			}
		}
	}

	if len(targets) == 0 {
		return emit(map[string]any{"status": "nothing_to_do"}, *jsonOut, "没有找到需要移除的注册项。")
	}

	if !*jsonOut {
		fmt.Fprintln(os.Stderr, "\n将移除以下注册：")
		for _, d := range targets {
			fmt.Fprintf(os.Stderr, "  %-16s %s\n", d.Spec.Name, d.Config)
		}
	}
	if !*yes && !*jsonOut {
		if !confirm("确认移除？") {
			fmt.Fprintln(os.Stderr, "已取消。")
			return 0
		}
	}

	var removed []string
	for _, d := range targets {
		if d.HasFile {
			if _, err := backupFile(d.Config); err != nil {
				fmt.Fprintf(os.Stderr, "备份失败，跳过 %s: %v\n", d.Spec.Name, err)
				continue
			}
		}
		var err error
		switch d.Spec.Format {
		case "toml":
			err = removeTOMLSection(d.Config)
		case "opencode":
			err = removeOpenCodeEntry(d.Config)
		default:
			err = removeJSONEntry(d.Config)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "移除失败 %s: %v\n", d.Spec.Name, err)
			continue
		}
		removed = append(removed, d.Spec.ID)
		if *removeSkill {
			if sd := d.Spec.skillDir(home); sd != "" {
				os.RemoveAll(filepath.Join(sd, skillDirName))
			}
		}
	}
	return emit(map[string]any{"status": "removed", "clients": removed}, *jsonOut,
		fmt.Sprintf("已移除：%s。请重启 Agent。", strings.Join(removed, ", ")))
}

func removeJSONEntry(p string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}
	m, ok := doc["mcpServers"].(map[string]any)
	if !ok {
		return nil
	}
	delete(m, serverEntryName)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(p, append(out, '\n'))
}

func removeTOMLSection(p string) error {
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	header := "[mcp_servers." + serverEntryName + "]"
	lines := strings.Split(string(b), "\n")
	out := make([]string, 0, len(lines))
	in := false
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			in = t == header
			if in {
				continue
			}
		}
		if in {
			continue
		}
		out = append(out, ln)
	}
	return writeAtomic(p, []byte(strings.Join(out, "\n")))
}

// ---------------------------------------------------------------------------
// 输出辅助
// ---------------------------------------------------------------------------

func emit(res any, jsonOut bool, humanMsg string) int {
	if jsonOut {
		b, _ := json.MarshalIndent(res, "", "  ")
		fmt.Println(string(b))
		return 0
	}
	if humanMsg != "" {
		fmt.Fprintln(os.Stderr, "\n"+humanMsg)
	}
	return 0
}

func fail(msg string, jsonOut bool) int {
	return emit(map[string]any{"status": "error", "message": msg}, jsonOut, "错误："+msg)
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `wxapkg-killer %s —— 微信小程序/小游戏解包 MCP Server

用法:
  wxapkg-killer                 以 MCP Server 模式运行（stdio）
  wxapkg-killer serve           同上
  wxapkg-killer setup           把本程序注册到本机 Agent，并安装工作流指令
  wxapkg-killer doctor          只读自检：注册状态与技能安装状态
  wxapkg-killer uninstall       移除注册与工作流指令
  wxapkg-killer version         打印版本

setup 参数:
  --client <id>       指定客户端，可重复
  --all-detected      配置所有探测到的客户端
  --dry-run           只输出计划，不写文件
  --json              以 JSON 输出结果
  --yes               免确认（需配合 --client 或 --all-detected）
  --skill=false       不安装工作流指令
  --config <path>     额外写入一个含 mcpServers 的 JSON 配置文件

支持的客户端: %s

环境变量:
  WXAPKG_OUT_ROOT     解包输出根目录，默认 ~/wxapkg-killer/out
`, version, strings.Join(supportedIDs(), ", "))
}
