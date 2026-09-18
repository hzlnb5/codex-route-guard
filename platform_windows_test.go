//go:build windows

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFreshRecoveryScriptOnlyStopsFreshMatchingFamily(t *testing.T) {
	script := freshCodexRecoveryScript(time.Now().UnixNano())
	for _, required := range []string{
		"$writeMs=",
		"Get-Family",
		"$targets=@($fresh | Where-Object { $_.Family -eq $family })",
		"Stop-Process -Id $_.Process.Id",
		"OpenAI.Codex_*",
		"OpenAI.ChatGPT*",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("recovery script lost safety guard %q", required)
		}
	}
	if strings.Contains(script, "Get-Process ChatGPT,Codex -ErrorAction SilentlyContinue | Stop-Process") {
		t.Fatal("recovery script must never terminate every ChatGPT/Codex process")
	}
	for _, forbidden := range []string{
		"if($p.ProcessName -eq 'Codex')",
		"Name -like 'Codex*'",
		"Name -like 'ChatGPT*'",
	} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("recovery script must fail closed instead of using fallback %q", forbidden)
		}
	}
	if !strings.Contains(script, "$families.Count -ne 1") {
		t.Fatal("recovery script must fail closed when multiple app families are fresh")
	}
	entryCheck := strings.Index(script, "if($matches.Count -ne 1){ exit 5 }")
	stop := strings.Index(script, "Stop-Process -Id $_.Process.Id")
	if entryCheck < 0 || stop < 0 || entryCheck > stop {
		t.Fatal("recovery must resolve exactly one restart target before terminating the fresh process")
	}
}

func TestDaemonRepairsCockpitRewriteWhileWebGPTIsLive(t *testing.T) {
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
	route := server.URL + "/v1"
	if err := os.WriteFile(configPath, []byte("model_provider = \"openai\"\nopenai_base_url = \""+route+"\"\n"), 0600); err != nil {
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

	done := make(chan error, 1)
	go func() { done <- runDaemon() }()
	defer func() {
		_ = os.MkdirAll(filepath.Dir(stopPath()), 0700)
		_ = os.WriteFile(stopPath(), []byte("stop"), 0600)
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("guard daemon did not stop")
		}
	}()

	time.Sleep(500 * time.Millisecond)
	if err := os.WriteFile(configPath, []byte("model_provider = \"openai\"\nmodel_catalog_json = \"cockpit-model-catalog.json\"\n"), 0600); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2500 * time.Millisecond)
	for time.Now().Before(deadline) {
		b, _ := os.ReadFile(configPath)
		text := string(b)
		if strings.Contains(text, "openai_base_url = \""+route+"\"") && !strings.Contains(text, "model_catalog_json") {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	b, _ := os.ReadFile(configPath)
	t.Fatalf("daemon did not repair Cockpit rewrite in time:\n%s", string(b))
}

func TestDaemonHonorsStopMarkerPresentAtStartup(t *testing.T) {
	root := t.TempDir()
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "local"))
	if err := os.MkdirAll(filepath.Dir(stopPath()), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stopPath(), []byte("stop"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- runDaemon() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon ignored stop marker present during startup")
	}
	if _, err := os.Stat(stopPath()); err != nil {
		t.Fatalf("daemon must not delete installer stop marker: %v", err)
	}
}
