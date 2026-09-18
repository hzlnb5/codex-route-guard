package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	appDirName            = "CodexRouteGuard"
	runRegistryName       = "CodexRouteGuard"
	legacyAppDirName      = "CodexWebGPTGuard"
	legacyRunRegistryName = "CodexWebGPTGuard"
	defaultMode           = "auto"
	officialOpenAI        = "https://api.openai.com/v1"
	nativeGrace           = 1500 * time.Millisecond
	presenceInterval      = 300 * time.Millisecond
)

var version = "dev"

var managedCatalogNames = map[string]bool{
	"cockpit-model-catalog.json":              true,
	"cockpit-provider-model-catalog.json":     true,
	"cockpit-local-access-model-catalog.json": true,
}

type settings struct {
	Mode string `json:"mode"`
}

type webGPTConfig struct {
	RuntimeCommand []string `json:"runtimeCommand"`
}

type integrationJournal struct {
	Version    int    `json:"version"`
	Active     bool   `json:"active"`
	ConfigPath string `json:"configPath"`
	Installed  struct {
		OpenAIBaseURL string `json:"openai_base_url"`
	} `json:"installed"`
}

type launcherDescriptor struct {
	Version  int    `json:"version"`
	Kind     string `json:"kind"`
	Profile  string `json:"profile"`
	PID      int    `json:"pid"`
	Endpoint string `json:"endpoint"`
}

type webState struct {
	Journal        *integrationJournal
	JournalPath    string
	DescriptorPath string
	Route          string
	HealthOK       bool
	LauncherLive   bool
	Presence       bool
	Reason         string
}

type repairResult struct {
	Changed  bool
	Conflict string
	Detail   string
}

type fileSignature struct {
	Size    int64
	ModTime int64
}

func main() {
	cmd := "run"
	if len(os.Args) > 1 {
		cmd = strings.ToLower(strings.TrimSpace(os.Args[1]))
	}

	var err error
	switch cmd {
	case "run":
		err = runDaemon()
	case "once":
		err = runOnce(true)
	case "status":
		err = printStatus()
	case "install":
		err = install()
	case "uninstall":
		err = uninstall()
	case "mode":
		if len(os.Args) < 3 {
			err = errors.New("usage: codex-route-guard.exe mode auto|web|native")
		} else {
			err = setMode(os.Args[2])
		}
	case "version", "--version", "-v":
		fmt.Println(version)
		return
	case "help", "-h", "--help":
		printHelp()
		return
	default:
		err = fmt.Errorf("unknown command %q", cmd)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "Codex Route Guard:", err)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Println("Codex Route Guard")
	fmt.Println("  version        print build version")
	fmt.Println("  install        install for current user and enable autostart")
	fmt.Println("  uninstall      disable autostart and stop background guard")
	fmt.Println("  run            run foreground daemon")
	fmt.Println("  once           perform one compatibility repair")
	fmt.Println("  status         show route ownership and Web GPT presence")
	fmt.Println("  mode auto      Web GPT when its launcher/runtime is live")
	fmt.Println("  mode web       force Web GPT route connected")
	fmt.Println("  mode native    safely disconnect Web GPT route using its own journal")
}

func appDir() (string, error) {
	base := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if base == "" {
		if runtime.GOOS != "windows" {
			return filepath.Join(os.TempDir(), appDirName), nil
		}
		return "", errors.New("LOCALAPPDATA is not set")
	}
	return filepath.Join(base, appDirName), nil
}

func settingsPath() string {
	d, _ := appDir()
	return filepath.Join(d, "settings.json")
}

func stopPath() string {
	d, _ := appDir()
	return filepath.Join(d, "stop.request")
}

func logPath() string {
	d, _ := appDir()
	return filepath.Join(d, "guard.log")
}

func legacyAppDir() string {
	base := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	if base == "" {
		return ""
	}
	return filepath.Join(base, legacyAppDirName)
}

func legacySettingsPath() string {
	d := legacyAppDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "settings.json")
}

func legacyStopPath() string {
	d := legacyAppDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "stop.request")
}

func loadLegacySettings() (settings, bool) {
	p := legacySettingsPath()
	if p == "" {
		return settings{}, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return settings{}, false
	}
	var s settings
	if json.Unmarshal(b, &s) != nil {
		return settings{}, false
	}
	s.Mode = normalizeMode(s.Mode)
	return s, true
}

func stopLegacyGuard() {
	if runtime.GOOS != "windows" {
		return
	}
	if p := legacyStopPath(); p != "" {
		_ = os.MkdirAll(filepath.Dir(p), 0700)
		_ = os.WriteFile(p, []byte(time.Now().Format(time.RFC3339Nano)), 0600)
	}
	_ = regDeleteRun(legacyRunRegistryName)
}

func webGPTHome() string {
	if v := strings.TrimSpace(os.Getenv("CODEX_CHATGPT_WEB_HOME")); v != "" {
		return expandHome(v)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex-chatgpt-web")
}

func defaultCodexConfig() string {
	if v := strings.TrimSpace(os.Getenv("CODEX_HOME")); v != "" {
		return filepath.Join(expandHome(v), "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".codex", "config.toml")
}

func expandHome(p string) string {
	p = strings.TrimSpace(p)
	home, _ := os.UserHomeDir()
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, "~\\") {
		return filepath.Join(home, p[2:])
	}
	return p
}

func normalizeMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "web":
		return "web"
	case "native":
		return "native"
	default:
		return "auto"
	}
}

func loadSettings() settings {
	b, err := os.ReadFile(settingsPath())
	if err != nil {
		return settings{Mode: defaultMode}
	}
	var s settings
	if json.Unmarshal(b, &s) != nil {
		return settings{Mode: defaultMode}
	}
	s.Mode = normalizeMode(s.Mode)
	return s
}

func saveSettings(s settings) error {
	s.Mode = normalizeMode(s.Mode)
	p := settingsPath()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(s, "", "  ")
	b = append(b, '\n')
	return atomicWrite(p, b)
}

func setMode(v string) error {
	raw := strings.ToLower(strings.TrimSpace(v))
	if raw != "auto" && raw != "web" && raw != "native" {
		return fmt.Errorf("invalid mode %q", v)
	}
	if err := saveSettings(settings{Mode: raw}); err != nil {
		return err
	}
	fmt.Println("mode:", raw)
	if err := runOnce(false); err != nil {
		return err
	}
	return nil
}

func install() error {
	if runtime.GOOS != "windows" {
		return errors.New("install is Windows-only")
	}

	// Migrate the older CodexWebGPTGuard package if it is installed. The old daemon
	// used a different install directory, startup value and mutex, so stop it before
	// starting Codex Route Guard to avoid two reconcilers touching the same config.
	stopLegacyGuard()
	time.Sleep(500 * time.Millisecond)
	d, err := appDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0700); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	self, _ = filepath.Abs(self)
	dst := filepath.Join(d, "codex-route-guard.exe")

	// Stop an older installed Guard before replacing its executable. Windows locks a
	// running image, so a self-update must give the existing background process a
	// chance to release the file first.
	_ = os.MkdirAll(filepath.Dir(stopPath()), 0700)
	_ = os.WriteFile(stopPath(), []byte(time.Now().Format(time.RFC3339Nano)), 0600)
	time.Sleep(450 * time.Millisecond)

	if !samePath(self, dst) {
		var copyErr error
		for i := 0; i < 12; i++ {
			copyErr = copyFile(self, dst)
			if copyErr == nil {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if copyErr != nil {
			return fmt.Errorf("replace installed Guard: %w", copyErr)
		}
	}
	if _, err := os.Stat(settingsPath()); errors.Is(err, os.ErrNotExist) {
		initial := settings{Mode: defaultMode}
		if legacy, ok := loadLegacySettings(); ok {
			initial = legacy
		}
		if err := saveSettings(initial); err != nil {
			return err
		}
	}
	_ = os.Remove(stopPath())
	if err := registerStartup(dst); err != nil {
		return err
	}
	if err := startHidden(dst, "run"); err != nil {
		return err
	}
	_ = runOnce(false)
	fmt.Println("Installed:", dst)
	fmt.Println("Mode: auto")
	return nil
}

func uninstall() error {
	if err := unregisterStartup(); err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Dir(stopPath()), 0700)
	_ = os.WriteFile(stopPath(), []byte(time.Now().Format(time.RFC3339Nano)), 0600)
	fmt.Println("Autostart disabled; background guard will stop shortly.")
	return nil
}

func runDaemon() error {
	if runtime.GOOS != "windows" {
		return errors.New("daemon is Windows-only")
	}
	release, already, err := acquireSingleInstance()
	if err != nil {
		return err
	}
	if already {
		return nil
	}
	defer release()
	_ = os.Remove(stopPath())

	var state webState
	var lastSig fileSignature
	var lastPresence bool
	var absentSince time.Time
	var lastJournal time.Time
	var lastProbe time.Time
	var lastSettings time.Time
	var lastRepair time.Time
	var lastConflict string
	var lastLifecycle time.Time
	s := loadSettings()
	logLine("guard started mode=%s", s.Mode)

	for {
		if fileExists(stopPath()) {
			logLine("guard stopping on request")
			return nil
		}
		now := time.Now()
		if now.Sub(lastSettings) >= time.Second {
			s = loadSettings()
			lastSettings = now
		}
		if now.Sub(lastJournal) >= 500*time.Millisecond {
			state = refreshJournal(state)
			lastJournal = now
		}
		if now.Sub(lastProbe) >= presenceInterval {
			state = refreshPresence(state, s.Mode)
			lastProbe = now
			if state.Presence != lastPresence {
				logLine("web presence=%t reason=%s mode=%s route=%s journal_active=%t", state.Presence, state.Reason, s.Mode, state.Route, state.Journal != nil && state.Journal.Active)
				lastPresence = state.Presence
			}
			if state.Presence {
				absentSince = time.Time{}
			} else if absentSince.IsZero() {
				absentSince = now
			}
		}

		// Reconcile the supported codex-chatgpt-web route lifecycle first. Native mode and
		// stable auto-mode absence use the project's own `route disconnect`, so its journal,
		// realtime route, hooks and models cache stay internally consistent. Web mode (or a
		// live launcher in auto mode) reconnects an inactive journal using `route connect`.
		if state.Journal != nil && now.Sub(lastLifecycle) >= 750*time.Millisecond {
			desiredWeb := s.Mode == "web" || (s.Mode == "auto" && state.Presence)
			desiredNative := s.Mode == "native" || (s.Mode == "auto" && !state.Presence && !absentSince.IsZero() && now.Sub(absentSince) >= nativeGrace)
			if desiredWeb && !state.Journal.Active {
				cfg := journalConfigPath(state)
				beforeConnect := statSignature(cfg)
				if err := routeAction(state, "connect"); err != nil {
					logLine("route connect failed: %v", err)
				} else {
					restarted := recoverFreshCodexLaunch(beforeConnect.ModTime)
					logLine("route connected through codex-chatgpt-web lifecycle restarted_fresh_codex=%t", restarted)
					state = refreshJournal(state)
					lastSig = fileSignature{}
				}
				lastLifecycle = now
			} else if desiredNative && state.Journal.Active {
				// Cockpit may have removed the Web route milliseconds before we got here. Restore
				// the one field Web GPT owns so its official disconnect can verify and restore the
				// exact previous state atomically.
				cfg := journalConfigPath(state)
				if _, err := repairConfig(cfg, state.Route); err != nil {
					logLine("pre-disconnect repair failed: %v", err)
				} else if err := routeAction(state, "disconnect"); err != nil {
					logLine("route disconnect failed: %v", err)
				} else {
					logLine("route disconnected through codex-chatgpt-web lifecycle")
					state = refreshJournal(state)
					lastSig = fileSignature{}
				}
				lastLifecycle = now
			}
		}

		// While Web mode is desired and the journal is active, close the race created by
		// Cockpit account projection: restore Web GPT's loopback route immediately after a
		// config write. This is intentionally narrow and refuses unknown route owners.
		desiredWeb := s.Mode == "web" || (s.Mode == "auto" && state.Presence)
		if desiredWeb && state.Journal != nil && state.Journal.Active && state.Route != "" {
			cfg := journalConfigPath(state)
			sig := statSignature(cfg)
			changed := sig != lastSig
			periodic := now.Sub(lastRepair) >= 2*time.Second
			if changed || periodic {
				res, err := repairConfig(cfg, state.Route)
				if err != nil {
					logLine("repair error: %v", err)
				} else {
					if res.Changed {
						restarted := recoverFreshCodexLaunch(sig.ModTime)
						logLine("route repaired: %s restarted_fresh_codex=%t", res.Detail, restarted)
						sig = statSignature(cfg)
					}
					if res.Conflict != "" && res.Conflict != lastConflict {
						logLine("route conflict: %s", res.Conflict)
						lastConflict = res.Conflict
					}
					if res.Conflict == "" {
						lastConflict = ""
					}
				}
				lastRepair = now
			}
			lastSig = sig
		} else {
			lastSig = fileSignature{}
		}

		if desiredWeb {
			time.Sleep(15 * time.Millisecond)
		} else {
			time.Sleep(120 * time.Millisecond)
		}
	}
}

func runOnce(verbose bool) error {
	s := loadSettings()
	state := refreshPresence(refreshJournal(webState{}), s.Mode)
	if verbose {
		fmt.Printf("mode=%s presence=%t reason=%s route=%s journal_active=%t\n", s.Mode, state.Presence, state.Reason, state.Route, state.Journal != nil && state.Journal.Active)
	}
	if state.Journal == nil {
		return nil
	}
	desiredWeb := s.Mode == "web" || (s.Mode == "auto" && state.Presence)
	if desiredWeb {
		if !state.Journal.Active {
			if err := routeAction(state, "connect"); err != nil {
				return err
			}
			state = refreshJournal(state)
		}
		res, err := repairConfig(journalConfigPath(state), state.Route)
		if verbose && err == nil {
			fmt.Printf("changed=%t conflict=%q detail=%s\n", res.Changed, res.Conflict, res.Detail)
		}
		return err
	}
	if s.Mode == "native" && state.Journal.Active {
		if _, err := repairConfig(journalConfigPath(state), state.Route); err != nil {
			return err
		}
		return routeAction(state, "disconnect")
	}
	return nil
}

func printStatus() error {
	s := loadSettings()
	state := refreshPresence(refreshJournal(webState{}), s.Mode)
	cfg := defaultCodexConfig()
	if state.Journal != nil && state.Journal.ConfigPath != "" {
		cfg = state.Journal.ConfigPath
	}
	b, _ := os.ReadFile(cfg)
	route, _ := topLevelString(string(b), "openai_base_url")
	provider, _ := topLevelString(string(b), "model_provider")
	catalog, _ := topLevelString(string(b), "model_catalog_json")
	fmt.Println("Mode:", s.Mode)
	fmt.Println("Web GPT journal:", state.JournalPath)
	fmt.Println("Journal active:", state.Journal != nil && state.Journal.Active)
	fmt.Println("Expected route:", state.Route)
	fmt.Println("Responses health:", state.HealthOK)
	fmt.Println("Launcher live:", state.LauncherLive)
	fmt.Println("Auto presence:", state.Presence, "("+state.Reason+")")
	fmt.Println("Codex config:", cfg)
	fmt.Println("model_provider:", provider)
	fmt.Println("openai_base_url:", route)
	fmt.Println("model_catalog_json:", catalog)
	return nil
}

func refreshJournal(prev webState) webState {
	home := webGPTHome()
	candidates := []string{
		filepath.Join(home, "codex", "integration-journal.json"),
		filepath.Join(home, "codex", "integration-journal.recovery.json"),
	}
	prev.DescriptorPath = filepath.Join(home, "runtime", "launcher-browser.json")
	prev.Journal = nil
	prev.JournalPath = ""
	prev.Route = ""
	for _, p := range candidates {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var j integrationJournal
		if json.Unmarshal(b, &j) != nil || j.Version < 3 {
			continue
		}
		route := strings.TrimRight(strings.TrimSpace(j.Installed.OpenAIBaseURL), "/")
		if !validLoopbackRoute(route) {
			continue
		}
		prev.Journal = &j
		prev.JournalPath = p
		prev.Route = route
		break
	}
	if prev.Journal == nil {
		prev.HealthOK = false
		prev.LauncherLive = false
		prev.Presence = false
		prev.Reason = "no integration journal"
	}
	return prev
}

func refreshPresence(prev webState, mode string) webState {
	mode = normalizeMode(mode)
	if prev.Journal == nil || prev.Route == "" {
		prev.Presence = false
		prev.Reason = "no integration journal"
		return prev
	}
	prev.HealthOK = routeHealthy(prev.Route)
	prev.LauncherLive = launcherLive(prev.DescriptorPath)
	if mode == "web" {
		prev.Presence = true
		prev.Reason = "forced web mode"
	} else if mode == "native" {
		prev.Presence = false
		prev.Reason = "forced native mode"
	} else if prev.HealthOK {
		prev.Presence = true
		prev.Reason = "responses health"
	} else if prev.LauncherLive {
		prev.Presence = true
		prev.Reason = "launcher descriptor"
	} else {
		prev.Presence = false
		prev.Reason = "Web GPT not running"
	}
	return prev
}

func journalConfigPath(state webState) string {
	if state.Journal != nil && strings.TrimSpace(state.Journal.ConfigPath) != "" {
		return state.Journal.ConfigPath
	}
	return defaultCodexConfig()
}

func loadWebGPTConfig() (webGPTConfig, error) {
	path := filepath.Join(webGPTHome(), "config.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return webGPTConfig{}, fmt.Errorf("read Web GPT config: %w", err)
	}
	var cfg webGPTConfig
	if err := json.Unmarshal(b, &cfg); err != nil {
		return webGPTConfig{}, fmt.Errorf("parse Web GPT config: %w", err)
	}
	if len(cfg.RuntimeCommand) == 0 || strings.TrimSpace(cfg.RuntimeCommand[0]) == "" {
		return webGPTConfig{}, errors.New("Web GPT config has no runtimeCommand")
	}
	return cfg, nil
}

func routeAction(state webState, action string) error {
	if action != "connect" && action != "disconnect" {
		return fmt.Errorf("invalid route action %q", action)
	}
	cfg, err := loadWebGPTConfig()
	if err != nil {
		return err
	}
	args := append([]string{}, cfg.RuntimeCommand[1:]...)
	args = append(args, "route", action)
	cmd := exec.Command(cfg.RuntimeCommand[0], args...)
	prepareHiddenCommand(cmd)
	cmd.Env = append(os.Environ(), "CODEX_CHATGPT_WEB_HOME="+webGPTHome())
	if state.Journal != nil && strings.TrimSpace(state.Journal.ConfigPath) != "" {
		cmd.Env = append(cmd.Env, "CODEX_HOME="+filepath.Dir(state.Journal.ConfigPath))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Web GPT route %s failed: %w: %s", action, err, strings.TrimSpace(string(out)))
	}
	var result struct {
		Active bool `json:"active"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		return fmt.Errorf("Web GPT route %s returned invalid JSON: %s", action, strings.TrimSpace(string(out)))
	}
	if action == "connect" && !result.Active {
		return errors.New("Web GPT route connect returned inactive")
	}
	if action == "disconnect" && result.Active {
		return errors.New("Web GPT route disconnect remained active")
	}
	return nil
}

func validLoopbackRoute(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "http" || u.Port() == "" {
		return false
	}
	host := strings.Trim(strings.ToLower(u.Hostname()), "[]")
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return false
	}
	return strings.TrimRight(u.Path, "/") == "/v1"
}

func routeHealthy(route string) bool {
	if !validLoopbackRoute(route) {
		return false
	}
	root := strings.TrimSuffix(strings.TrimRight(route, "/"), "/v1")
	client := &http.Client{Timeout: 220 * time.Millisecond}
	resp, err := client.Get(root + "/healthz")
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func launcherLive(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var d launcherDescriptor
	if json.Unmarshal(b, &d) != nil || d.PID <= 0 || d.Version < 3 || d.Kind != "codex-web-gpt-launcher" {
		return false
	}
	if !processAlive(d.PID) {
		return false
	}
	if strings.TrimSpace(d.Endpoint) == "" {
		return true
	}
	u, err := url.Parse(d.Endpoint)
	if err != nil || u.Scheme != "http" || u.Port() == "" {
		return false
	}
	host := strings.Trim(strings.ToLower(u.Hostname()), "[]")
	if host != "127.0.0.1" && host != "localhost" && host != "::1" {
		return false
	}
	conn, err := net.DialTimeout("tcp", u.Host, 120*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func repairConfig(path, expectedRoute string) (repairResult, error) {
	var out repairResult
	if !validLoopbackRoute(expectedRoute) {
		return out, fmt.Errorf("refusing invalid Web GPT route %q", expectedRoute)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	text := string(b)
	provider, _ := topLevelString(text, "model_provider")
	if provider != "" && provider != "openai" {
		out.Conflict = "model_provider is owned by " + provider
		return out, nil
	}
	catalog, catalogFound := topLevelString(text, "model_catalog_json")
	if catalogFound && catalog != "" && !managedCatalogNames[strings.ToLower(filepath.Base(catalog))] {
		out.Conflict = "external model_catalog_json is present: " + catalog
		return out, nil
	}
	current, routeFound := topLevelString(text, "openai_base_url")
	current = strings.TrimRight(strings.TrimSpace(current), "/")
	expectedRoute = strings.TrimRight(strings.TrimSpace(expectedRoute), "/")
	if routeFound && current != "" && !strings.EqualFold(current, officialOpenAI) && current != expectedRoute {
		out.Conflict = "another openai_base_url is present: " + current
		return out, nil
	}

	next := text
	removedCatalog := false
	if catalogFound && managedCatalogNames[strings.ToLower(filepath.Base(catalog))] {
		next = removeTopLevelKey(next, "model_catalog_json")
		removedCatalog = next != text
	}
	next = setTopLevelString(next, "openai_base_url", expectedRoute)
	if next == text {
		out.Detail = "already correct"
		return out, nil
	}
	if err := atomicWrite(path, []byte(next)); err != nil {
		return out, err
	}
	out.Changed = true
	if removedCatalog {
		out.Detail = "restored Web GPT route and removed Cockpit-managed model catalog"
	} else {
		out.Detail = "restored Web GPT route"
	}
	return out, nil
}

func topLevelString(text, key string) (string, bool) {
	for _, line := range logicalLines(text) {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			break
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		eq := strings.Index(trimmed, "=")
		if eq < 0 || strings.TrimSpace(trimmed[:eq]) != key {
			continue
		}
		raw := stripInlineComment(strings.TrimSpace(trimmed[eq+1:]))
		if raw == "" {
			return "", true
		}
		if strings.HasPrefix(raw, "\"") {
			if v, err := strconv.Unquote(raw); err == nil {
				return v, true
			}
		}
		if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
			return raw[1 : len(raw)-1], true
		}
		return raw, true
	}
	return "", false
}

func setTopLevelString(text, key, value string) string {
	eol := detectEOL(text)
	trailing := strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\r")
	bom := ""
	if strings.HasPrefix(text, "\ufeff") {
		bom = "\ufeff"
		text = strings.TrimPrefix(text, "\ufeff")
	}
	lines := logicalLines(text)
	escaped := strconv.Quote(value)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			lines = insertLine(lines, i, key+" = "+escaped)
			return bom + joinLines(lines, eol, trailing)
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		eq := strings.Index(trimmed, "=")
		if eq < 0 || strings.TrimSpace(trimmed[:eq]) != key {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		comment := inlineCommentSuffix(trimmed[eq+1:])
		newline := indent + key + " = " + escaped
		if comment != "" {
			newline += " " + comment
		}
		lines[i] = newline
		return bom + joinLines(lines, eol, trailing)
	}
	lines = append(lines, key+" = "+escaped)
	return bom + joinLines(lines, eol, true)
}

func removeTopLevelKey(text, key string) string {
	eol := detectEOL(text)
	trailing := strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\r")
	bom := ""
	if strings.HasPrefix(text, "\ufeff") {
		bom = "\ufeff"
		text = strings.TrimPrefix(text, "\ufeff")
	}
	lines := logicalLines(text)
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			break
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		eq := strings.Index(trimmed, "=")
		if eq >= 0 && strings.TrimSpace(trimmed[:eq]) == key {
			lines = append(lines[:i], lines[i+1:]...)
			break
		}
	}
	return bom + joinLines(lines, eol, trailing)
}

func logicalLines(text string) []string {
	text = strings.TrimPrefix(text, "\ufeff")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func detectEOL(text string) string {
	if strings.Contains(text, "\r\n") {
		return "\r\n"
	}
	if strings.Contains(text, "\r") {
		return "\r"
	}
	return "\n"
}

func joinLines(lines []string, eol string, trailing bool) string {
	out := strings.Join(lines, eol)
	if trailing {
		out += eol
	}
	return out
}

func insertLine(lines []string, index int, value string) []string {
	lines = append(lines, "")
	copy(lines[index+1:], lines[index:])
	lines[index] = value
	return lines
}

func stripInlineComment(raw string) string {
	if i := commentIndex(raw); i >= 0 {
		raw = raw[:i]
	}
	return strings.TrimSpace(raw)
}

func inlineCommentSuffix(raw string) string {
	if i := commentIndex(raw); i >= 0 {
		return strings.TrimSpace(raw[i:])
	}
	return ""
}

func commentIndex(s string) int {
	inBasic := false
	inLiteral := false
	escaped := false
	for i, r := range s {
		if inBasic {
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
			} else if r == '"' {
				inBasic = false
			}
			continue
		}
		if inLiteral {
			if r == '\'' {
				inLiteral = false
			}
			continue
		}
		if r == '"' {
			inBasic = true
		} else if r == '\'' {
			inLiteral = true
		} else if r == '#' {
			return i
		}
	}
	return -1
}

func statSignature(path string) fileSignature {
	st, err := os.Stat(path)
	if err != nil {
		return fileSignature{}
	}
	return fileSignature{Size: st.Size(), ModTime: st.ModTime().UnixNano()}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func samePath(a, b string) bool {
	aa, _ := filepath.Abs(a)
	bb, _ := filepath.Abs(b)
	return strings.EqualFold(filepath.Clean(aa), filepath.Clean(bb))
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err = out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return replaceFile(tmp, dst)
}

func logLine(format string, args ...any) {
	p := logPath()
	_ = os.MkdirAll(filepath.Dir(p), 0700)
	if st, err := os.Stat(p); err == nil && st.Size() > 1024*1024 {
		_ = os.Rename(p, p+".old")
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s ", time.Now().Format(time.RFC3339Nano))
	_, _ = fmt.Fprintf(f, format, args...)
	_, _ = fmt.Fprintln(f)
}

func registerStartup(exe string) error {
	ps := fmt.Sprintf("& '%s' run", strings.ReplaceAll(exe, "'", "''"))
	command := "powershell.exe -NoProfile -NonInteractive -WindowStyle Hidden -Command \"" + strings.ReplaceAll(ps, "\"", "\\\"") + "\""
	return regSetRun(runRegistryName, command)
}

func unregisterStartup() error {
	return regDeleteRun(runRegistryName)
}

func startHidden(exe, arg string) error {
	ps := fmt.Sprintf("Start-Process -WindowStyle Hidden -FilePath '%s' -ArgumentList '%s'", strings.ReplaceAll(exe, "'", "''"), strings.ReplaceAll(arg, "'", "''"))
	return exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-WindowStyle", "Hidden", "-Command", ps).Start()
}
