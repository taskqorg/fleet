package main

// Native commands for every harness, and codex exec for a run without an account.
// Native protocol shapes borrowed from AgentQ registry at d0f6bfa; no source copied.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
)

type harness struct{ name string }

func init() {
	for _, h := range []string{"codex", "claude", "antigravity", "acp"} {
		Harnesses[h] = harness{h}
	}
	NewBlocks = func() Blocks { return &blocks{tools: map[string]*openTool{}} }
}

// isACP: Antigravity, and kind `acp`, any agent the host binds with {executable, args, environment} (people-access §4).
func isACP(h string) bool { return h == "antigravity" || h == "acp" }

// pharness is harness::name for a payload: harness, else agentConfig.harness, else codex.
func pharness(p any) string {
	v, ok := any(nil), has(p, "harness")
	switch {
	case ok:
		v = jAt(p, "harness")
	case has(jAt(p, "agentConfig"), "harness"):
		v = jAt(p, "agentConfig", "harness")
	default:
		return "codex"
	}
	if s, ok := v.(string); ok {
		return s
	}
	return "invalid"
}

func absolute(p string) (string, error) {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	return filepath.Abs(r)
}

// private: absolute, not a link, no group/other permission bits.
func private(p string) error {
	st, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(p) || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm()&0o077 != 0 {
		return errors.New("native configuration must use private absolute paths")
	}
	return nil
}

func owner(st os.FileInfo) int { return int(st.Sys().(*syscall.Stat_t).Uid) }

func newCmd(exe, dir string) *exec.Cmd {
	c := exec.Command(exe)
	c.Env = []string{}
	c.Dir = dir
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return c
}

func setEnv(c *exec.Cmd, k, v string) {
	for i, e := range c.Env {
		if strings.HasPrefix(e, k+"=") {
			c.Env[i] = k + "=" + v
			return
		}
	}
	c.Env = append(c.Env, k+"="+v)
}

func passEnv(c *exec.Cmd, keys ...string) {
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			setEnv(c, k, v)
		}
	}
}

func (h harness) Command(dir, native string, p map[string]any) (*exec.Cmd, []byte, error) {
	n := pharness(p)
	if (n != "antigravity" && has(p, "settings")) || has(p, "environment") || (n == "codex" && has(p, "tools")) {
		return nil, nil, errors.New("requested native settings/environment/tools mapping unsupported")
	}
	if !(n == "codex" || n == "claude" || isACP(n)) {
		return nil, nil, errors.New("unknown harness")
	}
	if n == "codex" && !has(p, "account") {
		text, ok := str(p["text"])
		if !ok {
			return nil, nil, errors.New("missing text")
		}
		home, account := os.Getenv("FLEET_NATIVE_HOME"), os.Getenv("FLEET_CODEX_HOME")
		if home == "" {
			return nil, nil, errors.New("FLEET_NATIVE_HOME required")
		}
		if account == "" {
			return nil, nil, errors.New("FLEET_CODEX_HOME required")
		}
		exe := os.Getenv("FLEET_CODEX_BIN")
		if exe == "" {
			exe = "codex"
		}
		c, err := codexExec(dir, native, text, home, account, exe)
		return c, nil, err
	}
	registry := os.Getenv("FLEET_NATIVE_ACCOUNTS")
	if registry == "" {
		return nil, nil, errors.New("FLEET_NATIVE_ACCOUNTS required")
	}
	return commandRegistry(dir, native, p, registry)
}

// codexExec: `codex exec --json` with Fleet's fixed policy flags.
func codexExec(dir, native, text, home, account, exe string) (*exec.Cmd, error) {
	for _, p := range []string{home, account} {
		st, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if !filepath.IsAbs(p) || !st.IsDir() || st.Mode().Perm()&0o077 != 0 {
			return nil, errors.New("Fleet native homes must be absolute private directories")
		}
	}
	h, err := absolute(home)
	if err != nil {
		return nil, err
	}
	a, err := absolute(account)
	if err != nil {
		return nil, err
	}
	marker := filepath.Join(dir, ".fleet-native-home.json")
	binding := enc(M{"home": h, "codexHome": a})
	if native != "" {
		if old, err := os.ReadFile(marker); err != nil || string(old) != binding {
			return nil, errors.New("native session home mismatch; legacy history retained without migration")
		}
	}
	if err := os.WriteFile(marker, []byte(binding), 0o644); err != nil {
		return nil, err
	}
	c := newCmd(exe, dir)
	passEnv(c, "PATH", "USER", "LOGNAME", "TMPDIR", "LANG", "LC_ALL", "SSL_CERT_FILE", "SSL_CERT_DIR")
	setEnv(c, "HOME", home)
	setEnv(c, "CODEX_HOME", account)
	c.Args = append(c.Args, "exec", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check", "--json",
		"-c", "project_doc_max_bytes=0", "-c", "skills.enabled=false", "-c", "skills.include_instructions=false", "-c", "mcp_servers={}",
		"--disable", "memories", "--disable", "hooks", "--enable", "skip_host_skill_discovery",
		"-c", `forced_login_method="chatgpt"`, "-c", `approval_policy="never"`, "-c", `sandbox_mode="workspace-write"`,
		"-c", "sandbox_workspace_write.network_access=false",
		"--disable", "apps", "--disable", "plugins", "--disable", "browser_use", "--disable", "computer_use", "--disable", "image_generation")
	if native != "" {
		c.Args = append(c.Args, "resume", native)
	}
	c.Args = append(c.Args, text)
	return c, nil
}

func commandRegistry(dir, native string, p0 map[string]any, path string) (*exec.Cmd, []byte, error) {
	fail := func(s string) (*exec.Cmd, []byte, error) { return nil, nil, errors.New(s) }
	p := jClone(p0).(M)
	if profile, ok := p0["agentConfig"]; ok {
		if err := profileValidate(profile); err != nil {
			return nil, nil, err
		}
		for _, k := range []string{"harness", "model", "effort"} {
			if has(p0, k) && has(profile, k) && enc(p0[k]) != enc(jAt(profile, k)) {
				return fail("requested configuration conflicts with profile")
			}
		}
		for _, k := range []string{"harness", "model", "effort", "instructions", "instructionRole", "tools", "settings"} {
			if has(profile, k) {
				p[k] = jAt(profile, k)
			}
		}
	}
	h := pharness(p)
	if err := private(path); err != nil {
		return nil, nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if owner(st) != os.Geteuid() {
		return fail("registry must be owned by executor user")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	accounts, err := decode(raw)
	if err != nil {
		return nil, nil, err
	}
	account, ok := str(p["account"])
	if !ok {
		return fail("named account required")
	}
	if !has(accounts, account) {
		return fail("account not provisioned")
	}
	a := jAt(accounts, account)
	if has(a, "readiness") && jAt(a, "readiness") != "ready" {
		return fail("named host account is unprovisioned; operator login and readiness review required")
	}
	if jAt(a, "harness") != h {
		return fail("account harness mismatch")
	}
	home, ok := str(jAt(a, "home"))
	if !ok {
		return fail("home missing")
	}
	nh, ok := str(jAt(a, "nativeHome"))
	if !ok {
		return fail("nativeHome missing")
	}
	for _, d := range []string{home, nh} {
		if err := private(d); err != nil {
			return nil, nil, err
		}
	}
	for _, d := range []string{home, nh} {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			return fail("homes must be directories")
		}
	}
	credentialHome := ""
	if (isACP(h) || h == "claude") && jAt(a, "credentialAuthority") == "native-current" {
		ch, ok := str(jAt(a, "credentialHome"))
		if !ok {
			return fail("credentialHome required for native-current")
		}
		st, err := os.Lstat(ch)
		if err != nil {
			return nil, nil, err
		}
		if !filepath.IsAbs(ch) || !st.IsDir() || owner(st) != os.Geteuid() || st.Mode().Perm()&0o022 != 0 {
			return fail("native-current credential home must be owner-controlled")
		}
		credentialHome = ch
	} else if has(a, "credentialAuthority") {
		return fail("unsupported credential authority")
	}
	exe, ok := str(jAt(a, "executable"))
	if !ok {
		return fail("host executable missing")
	}
	if !filepath.IsAbs(exe) {
		return fail("host executable must be absolute")
	}
	homeAbs, err := absolute(home)
	if err != nil {
		return nil, nil, err
	}
	nhAbs, err := absolute(nh)
	if err != nil {
		return nil, nil, err
	}
	binding := M{"harness": h, "account": account, "home": homeAbs, "nativeHome": nhAbs}
	if h == "claude" {
		if _, err := claudePlacementHere(); err != nil {
			return fail("Claude is ineligible on this host: " + err.Error())
		}
	}
	if isACP(h) || h == "claude" {
		binding["transport"] = map[bool]string{true: "stream-json", false: "acp-1.5.3"}[h == "claude"]
		binding["credentialAuthority"] = jAt(a, "credentialAuthority")
		binding["credentialHome"] = nil
		if credentialHome != "" {
			c, err := absolute(credentialHome)
			if err != nil {
				return nil, nil, err
			}
			binding["credentialHome"] = c
		}
	}
	marker := filepath.Join(dir, fmt.Sprintf(".fleet-harness-account.%s.%s.json", h, account))
	if native != "" {
		old, err := os.ReadFile(marker)
		if err != nil {
			old, err = os.ReadFile(filepath.Join(dir, ".fleet-harness-account.json"))
		}
		v, derr := decode(old)
		if err != nil || derr != nil || enc(v) != enc(binding) {
			return fail("native account/session binding mismatch")
		}
	}
	if err := os.WriteFile(marker, []byte(enc(binding)), 0o644); err != nil {
		return nil, nil, err
	}
	text, hasText := str(p["text"])
	if h == "codex" {
		if !hasText {
			return fail("missing text")
		}
		c, err := codexExec(dir, native, text, home, nh, exe)
		return c, nil, err
	}
	if (h != "antigravity" && has(p, "settings")) || has(p, "environment") {
		return fail("unmapped native settings/environment")
	}
	role := map[bool]string{true: "system", false: "user"}[h == "claude"]
	if has(p, "instructionRole") && p["instructionRole"] != role {
		return fail("requested instruction role unsupported by native adapter")
	}
	if h == "claude" && p["effort"] != nil {
		return fail("effort unsupported by selected native adapter")
	}
	model, _ := str(p["model"])
	if model == "" || strings.HasPrefix(model, "-") {
		return fail("explicit native model required")
	}
	c := newCmd(exe, dir)
	setEnv(c, "HOME", home)
	passEnv(c, "PATH", "TMPDIR", "LANG", "SSL_CERT_FILE", "SSL_CERT_DIR")
	if !hasText {
		return fail("missing text")
	}
	instructions := jStr(p["instructions"])
	if h == "claude" {
		u, err := user.Current()
		if err != nil || u.Username == "" {
			return fail("host user metadata unavailable")
		}
		setEnv(c, "USER", u.Username)
		setEnv(c, "LOGNAME", u.Username)
		if credentialHome != "" {
			setEnv(c, "HOME", credentialHome)
		}
		// Skills (Lab Stage 2): written as a local plugin and loaded with --plugin-dir; slash commands stay disabled otherwise.
		skills, _ := p["skills"].([]any)
		if len(skills) == 0 {
			c.Args = append(c.Args, "--disable-slash-commands")
		} else {
			plugin := filepath.Join(dir, ".fleet-skills")
			os.RemoveAll(plugin)
			if err := os.MkdirAll(filepath.Join(plugin, ".claude-plugin"), 0o755); err != nil {
				return nil, nil, err
			}
			manifest := enc(M{"name": "fleet-skills", "version": "1.0.0", "description": "Skills granted by the Fleet profile"})
			if err := os.WriteFile(filepath.Join(plugin, ".claude-plugin/plugin.json"), []byte(manifest), 0o644); err != nil {
				return nil, nil, err
			}
			for _, s := range skills {
				id, _ := str(jAt(s, "id"))
				if id == "" || strings.Trim(id, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
					return fail("invalid skill id")
				}
				d := filepath.Join(plugin, "skills", id)
				if err := os.MkdirAll(d, 0o755); err != nil {
					return nil, nil, err
				}
				if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(jStr(jAt(s, "text"))), 0o644); err != nil {
					return nil, nil, err
				}
			}
			c.Args = append(c.Args, "--plugin-dir", plugin)
		}
		setEnv(c, "CLAUDE_CONFIG_DIR", nh)
		c.Args = append(c.Args, "-p", "--output-format", "stream-json", "--verbose", "--model", model, "--permission-mode", "dontAsk",
			"--strict-mcp-config", "--setting-sources", "", "--system-prompt", instructions)
		var tools []string
		if has(p, "tools") {
			list, ok := p["tools"].([]any)
			if !ok {
				return fail("unsupported Claude tool grant")
			}
			for _, t := range list {
				s, _ := str(t)
				switch s {
				case "Read", "Edit", "Write", "Bash", "Glob", "Grep":
					tools = append(tools, s)
				default:
					return fail("unsupported Claude tool grant")
				}
			}
		}
		tools = append(tools, claudeExtraTools(p)...)
		c.Args = append(c.Args, "--tools", strings.Join(tools, ","))
		if native != "" {
			if !safeNativeID(native) {
				return fail("invalid native session id")
			}
			c.Args = append(c.Args, "--resume="+native)
		} else if src, ok := str(jAt(p, "fork", "nativeSessionId")); ok && safeNativeID(src) {
			// Claude stores sessions per cwd project; copy the source transcript into this session's project, then fork it.
			file := src + ".jsonl"
			entries, err := os.ReadDir(filepath.Join(nh, "projects"))
			if err != nil {
				return nil, nil, err
			}
			found := ""
			for _, e := range entries {
				f := filepath.Join(nh, "projects", e.Name(), file)
				if st, err := os.Stat(f); err == nil && st.Mode().IsRegular() {
					found = f
					break
				}
			}
			if found == "" {
				return fail("native fork source not found")
			}
			d, err := absolute(dir)
			if err != nil {
				return nil, nil, err
			}
			enc := []rune(d)
			for i, ch := range enc {
				if !(ch < 128 && (ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9')) {
					enc[i] = '-'
				}
			}
			target := filepath.Join(nh, "projects", string(enc))
			if err := os.MkdirAll(target, 0o755); err != nil {
				return nil, nil, err
			}
			if filepath.Dir(found) != target {
				b, err := os.ReadFile(found)
				if err != nil {
					return nil, nil, err
				}
				if err := os.WriteFile(filepath.Join(target, file), b, 0o600); err != nil {
					return nil, nil, err
				}
			}
			c.Args = append(c.Args, "--resume="+src, "--fork-session")
		}
		// Variadic native tool flags must never consume the user prompt: it goes to stdin.
		return c, []byte(text + "\n"), nil
	}
	if err := validateAntigravitySettings(p["settings"]); err != nil {
		return nil, nil, err
	}
	if p["effort"] != nil {
		return fail("ACP effort must be selected explicitly through an advertised model ID")
	}
	if credentialHome != "" {
		setEnv(c, "HOME", credentialHome)
	}
	// The binding's args and environment, with {nativeHome} replaced; Antigravity's built-in binding when none is given.
	sub := func(v string) string { return strings.ReplaceAll(v, "{nativeHome}", nh) }
	var args []string
	if has(a, "args") {
		list, ok := jAt(a, "args").([]any)
		for _, x := range list {
			s, isS := x.(string)
			ok = ok && isS
			args = append(args, sub(s))
		}
		if !ok {
			return fail("binding args must be strings")
		}
	} else if h == "acp" {
		return fail("acp binding needs args")
	} else {
		for _, x := range []string{"acp", "--gemini-dir", "{nativeHome}", "--app-data-dir", "antigravity-cli", "--include-default-customizations=false"} {
			args = append(args, sub(x))
		}
	}
	if has(a, "environment") {
		env, ok := jAt(a, "environment").(M)
		if !ok {
			return fail("binding environment must be an object")
		}
		for k, x := range env {
			s, ok := x.(string)
			if !ok {
				return fail("binding environment values must be strings")
			}
			setEnv(c, k, sub(s))
		}
	} else if h != "acp" {
		setEnv(c, "GEMINI_HOME", nh)
	}
	c.Args = append(c.Args, args...)
	if sandbox, ok := jAt(p, "settings", "sandbox").(bool); ok && h == "antigravity" {
		c.Args = append(c.Args, fmt.Sprintf("--sandbox=%t", sandbox))
	}
	return c, nil, nil
}

// claudeExtraTools: built-in Claude tools a run adds to its grant: web only with `webRoute: builtin` (R3.4), Skill when
// the profile grants skills.
func claudeExtraTools(p any) []string {
	var t []string
	if jAt(p, "webRoute") == "builtin" {
		t = append(t, "WebSearch", "WebFetch")
	}
	if s, _ := jAt(p, "skills").([]any); len(s) > 0 {
		t = append(t, "Skill")
	}
	return t
}
