package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepairConfigPreservesWebGPTRoute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	input := "model_provider = \"openai\"\r\n\r\n[features]\r\ngoals = true\r\n"
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := repairConfig(path, "http://127.0.0.1:17841/v1")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed {
		t.Fatalf("expected change: %+v", res)
	}
	out, _ := os.ReadFile(path)
	text := string(out)
	if !strings.Contains(text, "openai_base_url = \"http://127.0.0.1:17841/v1\"\r\n[features]") {
		t.Fatalf("route not inserted before table:\n%s", text)
	}
}

func TestRepairConfigRemovesCockpitManagedCatalog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	input := "model_provider = \"openai\"\nmodel_catalog_json = \"cockpit-model-catalog.json\"\nopenai_base_url = \"https://api.openai.com/v1\"\n"
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := repairConfig(path, "http://127.0.0.1:17841/v1")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || !strings.Contains(res.Detail, "model catalog") {
		t.Fatalf("unexpected: %+v", res)
	}
	out, _ := os.ReadFile(path)
	text := string(out)
	if strings.Contains(text, "model_catalog_json") {
		t.Fatalf("managed catalog remained:\n%s", text)
	}
	if !strings.Contains(text, "openai_base_url = \"http://127.0.0.1:17841/v1\"") {
		t.Fatalf("route missing:\n%s", text)
	}
}

func TestRepairConfigRefusesCustomProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	input := "model_provider = \"sub2api\"\nopenai_base_url = \"http://127.0.0.1:9900/v1\"\n"
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := repairConfig(path, "http://127.0.0.1:17841/v1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflict == "" || res.Changed {
		t.Fatalf("expected provider conflict: %+v", res)
	}
	out, _ := os.ReadFile(path)
	if string(out) != input {
		t.Fatal("custom provider config modified")
	}
}

func TestRepairConfigRefusesUnknownCatalog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	input := "model_provider = \"openai\"\nmodel_catalog_json = \"my-models.json\"\n"
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := repairConfig(path, "http://127.0.0.1:17841/v1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflict == "" || res.Changed {
		t.Fatalf("expected catalog conflict: %+v", res)
	}
}

func TestRepairConfigRefusesOtherRoute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	input := "model_provider = \"openai\"\nopenai_base_url = \"http://127.0.0.1:9999/v1\"\n"
	if err := os.WriteFile(path, []byte(input), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := repairConfig(path, "http://127.0.0.1:17841/v1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Conflict == "" || res.Changed {
		t.Fatalf("expected route conflict: %+v", res)
	}
}

func TestNestedKeysAreNotTopLevel(t *testing.T) {
	input := "[model_providers.foo]\nmodel_provider = \"nested\"\nopenai_base_url = \"http://127.0.0.1:1/v1\"\n"
	if _, ok := topLevelString(input, "model_provider"); ok {
		t.Fatal("nested key treated as top-level")
	}
	out := setTopLevelString(input, "openai_base_url", "http://127.0.0.1:17841/v1")
	if !strings.HasPrefix(out, "openai_base_url = \"http://127.0.0.1:17841/v1\"\n[model_providers.foo]") {
		t.Fatalf("bad insertion:\n%s", out)
	}
}

func TestInlineCommentPreserved(t *testing.T) {
	input := "openai_base_url = \"https://api.openai.com/v1\" # keep me\n"
	out := setTopLevelString(input, "openai_base_url", "http://127.0.0.1:17841/v1")
	if !strings.Contains(out, "# keep me") {
		t.Fatalf("comment lost: %s", out)
	}
}

func TestValidLoopbackRoute(t *testing.T) {
	good := []string{"http://127.0.0.1:17841/v1", "http://localhost:17841/v1/", "http://[::1]:17841/v1"}
	for _, v := range good {
		if !validLoopbackRoute(v) {
			t.Fatalf("expected valid: %s", v)
		}
	}
	bad := []string{"https://127.0.0.1:17841/v1", "http://example.com:17841/v1", "http://127.0.0.1/v1", "http://127.0.0.1:17841/other"}
	for _, v := range bad {
		if validLoopbackRoute(v) {
			t.Fatalf("expected invalid: %s", v)
		}
	}
}

func TestAutoModeRepairsLiveWebGPTIntegration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	root := t.TempDir()
	local := filepath.Join(root, "local")
	web := filepath.Join(root, "web")
	codex := filepath.Join(root, "codex")
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("CODEX_CHATGPT_WEB_HOME", web)
	t.Setenv("CODEX_HOME", codex)

	if err := os.MkdirAll(filepath.Join(web, "codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codex, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(codex, "config.toml")
	if err := os.WriteFile(configPath, []byte("model_provider = \"openai\"\nmodel_catalog_json = \"cockpit-model-catalog.json\"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	journal := integrationJournal{Version: 10, Active: true, ConfigPath: configPath}
	journal.Installed.OpenAIBaseURL = server.URL + "/v1"
	jb, _ := json.Marshal(journal)
	if err := os.WriteFile(filepath.Join(web, "codex", "integration-journal.json"), jb, 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveSettings(settings{Mode: "auto"}); err != nil {
		t.Fatal(err)
	}

	if err := runOnce(false); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(configPath)
	text := string(out)
	if !strings.Contains(text, "openai_base_url = \""+server.URL+"/v1\"") {
		t.Fatalf("auto mode did not restore live Web GPT route:\n%s", text)
	}
	if strings.Contains(text, "model_catalog_json") {
		t.Fatalf("Cockpit catalog was not removed:\n%s", text)
	}
}

func TestAutoModeLeavesNativeConfigWhenWebGPTIsDown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	route := server.URL + "/v1"
	server.Close()

	root := t.TempDir()
	local := filepath.Join(root, "local")
	web := filepath.Join(root, "web")
	codex := filepath.Join(root, "codex")
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("CODEX_CHATGPT_WEB_HOME", web)
	t.Setenv("CODEX_HOME", codex)

	if err := os.MkdirAll(filepath.Join(web, "codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codex, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(codex, "config.toml")
	original := "model_provider = \"openai\"\n"
	if err := os.WriteFile(configPath, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	journal := integrationJournal{Version: 10, Active: true, ConfigPath: configPath}
	journal.Installed.OpenAIBaseURL = route
	jb, _ := json.Marshal(journal)
	if err := os.WriteFile(filepath.Join(web, "codex", "integration-journal.json"), jb, 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveSettings(settings{Mode: "auto"}); err != nil {
		t.Fatal(err)
	}

	if err := runOnce(false); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(configPath)
	if string(out) != original {
		t.Fatalf("auto mode changed native config while Web GPT was down:\n%s", string(out))
	}
}

func TestRouteLifecycleHelper(t *testing.T) {
	if os.Getenv("GO_WANT_WEBGPT_ROUTE_HELPER") != "1" {
		return
	}
	args := os.Args
	if len(args) < 2 {
		os.Exit(2)
	}
	action := args[len(args)-1]
	if len(args) < 2 || args[len(args)-2] != "route" || (action != "connect" && action != "disconnect") {
		os.Exit(3)
	}
	web := os.Getenv("CODEX_CHATGPT_WEB_HOME")
	journalPath := filepath.Join(web, "codex", "integration-journal.json")
	b, err := os.ReadFile(journalPath)
	if err != nil {
		os.Exit(4)
	}
	var journal integrationJournal
	if json.Unmarshal(b, &journal) != nil {
		os.Exit(5)
	}
	cfg, err := os.ReadFile(journal.ConfigPath)
	if err != nil {
		os.Exit(6)
	}
	text := string(cfg)
	if action == "connect" {
		text = setTopLevelString(text, "openai_base_url", journal.Installed.OpenAIBaseURL)
		journal.Active = true
	} else {
		text = removeTopLevelKey(text, "openai_base_url")
		journal.Active = false
	}
	if err := os.WriteFile(journal.ConfigPath, []byte(text), 0600); err != nil {
		os.Exit(7)
	}
	jb, _ := json.Marshal(journal)
	if err := os.WriteFile(journalPath, jb, 0600); err != nil {
		os.Exit(8)
	}
	_, _ = os.Stdout.Write([]byte(`{"active":` + map[bool]string{true: "true", false: "false"}[journal.Active] + `}`))
	os.Exit(0)
}

func lifecycleFixture(t *testing.T, active bool, mode string) (configPath, journalPath, route string) {
	t.Helper()
	root := t.TempDir()
	local := filepath.Join(root, "local")
	web := filepath.Join(root, "web")
	codex := filepath.Join(root, "codex")
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("CODEX_CHATGPT_WEB_HOME", web)
	t.Setenv("CODEX_HOME", codex)
	t.Setenv("GO_WANT_WEBGPT_ROUTE_HELPER", "1")
	if err := os.MkdirAll(filepath.Join(web, "codex"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codex, 0700); err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(codex, "config.toml")
	route = "http://127.0.0.1:17841/v1"
	config := "model_provider = \"openai\"\n"
	if active {
		config += "openai_base_url = \"" + route + "\"\n"
	}
	if err := os.WriteFile(configPath, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	journal := integrationJournal{Version: 10, Active: active, ConfigPath: configPath}
	journal.Installed.OpenAIBaseURL = route
	jb, _ := json.Marshal(journal)
	journalPath = filepath.Join(web, "codex", "integration-journal.json")
	if err := os.WriteFile(journalPath, jb, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := webGPTConfig{RuntimeCommand: []string{os.Args[0], "-test.run=TestRouteLifecycleHelper", "--"}}
	cb, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(web, "config.json"), cb, 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveSettings(settings{Mode: mode}); err != nil {
		t.Fatal(err)
	}
	return
}

func TestNativeModeUsesOfficialRouteDisconnect(t *testing.T) {
	configPath, journalPath, route := lifecycleFixture(t, true, "native")
	if err := runOnce(false); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(configPath)
	if strings.Contains(string(cfg), route) || strings.Contains(string(cfg), "openai_base_url") {
		t.Fatalf("native mode did not disconnect Web GPT route:\n%s", string(cfg))
	}
	jb, _ := os.ReadFile(journalPath)
	var journal integrationJournal
	_ = json.Unmarshal(jb, &journal)
	if journal.Active {
		t.Fatal("official route disconnect did not mark the journal inactive")
	}
}

func TestWebModeUsesOfficialRouteConnect(t *testing.T) {
	configPath, journalPath, route := lifecycleFixture(t, false, "web")
	if err := runOnce(false); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(configPath)
	if !strings.Contains(string(cfg), `openai_base_url = "`+route+`"`) {
		t.Fatalf("web mode did not connect Web GPT route:\n%s", string(cfg))
	}
	jb, _ := os.ReadFile(journalPath)
	var journal integrationJournal
	_ = json.Unmarshal(jb, &journal)
	if !journal.Active {
		t.Fatal("official route connect did not mark the journal active")
	}
}

func TestLauncherDescriptorRejectsWrongKindWithoutDialing(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "launcher-browser.json")
	d := launcherDescriptor{
		Version:  3,
		Kind:     "other-launcher",
		PID:      os.Getpid(),
		Endpoint: "http://example.com:80",
	}
	b, _ := json.Marshal(d)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if launcherLive(path) {
		t.Fatal("foreign launcher descriptor must not be accepted")
	}
}

func TestLoadLegacySettingsForMigration(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", root)
	legacyDir := filepath.Join(root, legacyAppDirName)
	if err := os.MkdirAll(legacyDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "settings.json"), []byte(`{"mode":"web"}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, ok := loadLegacySettings()
	if !ok || got.Mode != "web" {
		t.Fatalf("legacy settings migration failed: ok=%t mode=%q", ok, got.Mode)
	}
}
