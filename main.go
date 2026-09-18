package main

import (
	"context"
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
	// A stop marker may have been written by a concurrent installer before this daemon
	// finished starting. Never clear it here: doing so can strand the just-started
	// process and keep the installed executable locked during a self-update.
	if fileExists(stopPath()) {
		logLine("guard startup cancelled by stop request")
		return nil
	}

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
		if now.Sub(lastJournal) >= time.Second {
			state = refreshJournal(state)
			lastJournal = now
		}
		if now.Sub(lastProbe) >= presenceInterval {
			state = refreshPresence(state, s.Mode)
			lastProbe = now
			if state.Presence != lastPresence {
				logLine("web presence=%t reason=%s mode=%s route=%s", state.Presence, state.Reason, s.Mode, state.Route)
			lastPresence = state.Presence
		}
		}

		if s.Mode == "auto" {
			if state.Presence {
				absentSince = time.Time{}
			} else if absentSince.IsZero() {
				absentSince = now
			}
		} else {
			absentSince = time.Time{}
		}

		if state.Journal != nil && now.Sub(lastLifecycle) >= 200*time.Millisecond {
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
		} else if desiredNative && state.Journal.Active {
				if err := routeAction(state, "disconnect"); err != nil {
					logLine("route disconnect failed: %v", err)
				} else {
					logLine("route disconnected through codex-chatgpt-web lifecycle")
					state = refreshJournal(state)
					lastSig = fileSignature{}
				}
		}
			lastLifecycle = now
		}

		cfgPath := journalConfigPath(state)
		sig := statSignature(cfgPath)
		changedOnDisk := sig != lastSig
		periodicRepair := now.Sub(lastRepair) >= 2*time.Second
		if state.Journal != nil && state.Journal.Active && state.Presence && state.Route != "" && (changedOnDisk || periodicRepair) {
		res, err := repairConfig(cfgPath, state.Route)
		if err != nil {
			logLine("repair error: %v", err)
		} else {
			if res.Changed {
				logLine("route repaired: %s", res.Detail)
				lastSig = statSignature(cfgPath)
				if changedOnDisk {
					restarted := recoverFreshCodexLaunch(sig.ModTime)
					logLine("launch race recovery=%t", restarted)
				}
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

		if !(changedOnDisk && lastSig != fileSignature{}) {
		lastSig = sig
		}
	time.Sleep(20 * time.Millisecond)
	}
}
