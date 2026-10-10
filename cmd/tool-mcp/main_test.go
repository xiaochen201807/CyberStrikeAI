package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/toolcatalog"
)

func TestValidateTarget(t *testing.T) {
	valid := []string{"https://example.com", "http://127.0.0.1:8080/healthz"}
	for _, target := range valid {
		if err := validateTarget(target); err != nil {
			t.Errorf("validateTarget(%q): %v", target, err)
		}
	}
	invalid := []string{"example.com", "https://", "https://user:pass@example.com", "https://example.com\nheader"}
	for _, target := range invalid {
		if err := validateTarget(target); err == nil {
			t.Errorf("validateTarget(%q) accepted invalid target", target)
		}
	}
}

func TestTargetAllowlist(t *testing.T) {
	allowed, err := parseAllowedHosts("authorized.example.com,*.authorized.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTargetAgainst("https://authorized.example.com", allowed); err != nil {
		t.Fatalf("expected exact host to pass: %v", err)
	}
	if err := validateTargetAgainst("https://api.authorized.example.com/path", allowed); err != nil {
		t.Fatalf("expected subdomain wildcard to pass: %v", err)
	}
	if err := validateTargetAgainst("https://other.example.com", allowed); err == nil {
		t.Fatal("expected host outside allowlist to fail")
	}
	if _, err := parseAllowedHosts("*"); err == nil {
		t.Fatal("expected global wildcard to fail")
	}
	for _, raw := range []string{"*.", "*.example.*", "api.*.example.com", "bad host"} {
		if _, err := parseAllowedHosts(raw); err == nil {
			t.Errorf("expected invalid allowlist %q to fail", raw)
		}
	}
}

func TestValidateSeverity(t *testing.T) {
	if err := validateSeverity("medium, high,critical"); err != nil {
		t.Fatalf("expected valid severity list: %v", err)
	}
	if err := validateSeverity("all"); err == nil {
		t.Fatal("expected unsupported severity to fail")
	}
}

func TestBoundedInt(t *testing.T) {
	args := map[string]interface{}{"value": float64(999)}
	if got := boundedInt(args, "value", 10, 1, 100); got != 100 {
		t.Fatalf("expected upper bound, got %d", got)
	}
	if got := boundedInt(map[string]interface{}{}, "value", 10, 1, 100); got != 10 {
		t.Fatalf("expected fallback, got %d", got)
	}
}

func TestValidateServiceToken(t *testing.T) {
	if err := validateServiceToken("short"); err == nil {
		t.Fatal("expected short token to fail")
	}
	if err := validateServiceToken("replace-with-a-real-token-012345678901234567890"); err == nil {
		t.Fatal("expected placeholder token to fail")
	}
	if err := validateServiceToken("0123456789012345678901234567890123456789012345678901234567890123"); err != nil {
		t.Fatalf("expected long token to pass: %v", err)
	}
}

func TestSelectToolsUsesRepositoryDefinitionsForGenericMode(t *testing.T) {
	root := t.TempDir()
	content := `name: nmap
command: nmap
enabled: true
description: network scan
parameters:
  - name: target
    type: string
    required: true
    position: 1
    format: positional
`
	if err := os.WriteFile(filepath.Join(root, "nmap.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOOL_IDS", "nmap")
	t.Setenv("TOOL_DEFINITIONS_DIR", root)
	selected, generic, err := selectTools(toolcatalog.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	if !generic || len(selected) != 1 || selected[0].tool.ID != "nmap" {
		t.Fatalf("unexpected generic selection: generic=%v selected=%#v", generic, selected)
	}
	if selected[0].definition == nil || selected[0].definition.Parameters[0].Name != "target" {
		t.Fatalf("definition was not preserved: %#v", selected[0].definition)
	}
}

func TestGenericCommandRejectsShellDefinitions(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "shell.yaml"), []byte("name: shell\ncommand: sh\nenabled: true\ndescription: shell\n"), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TOOL_IDS", "shell")
	t.Setenv("TOOL_DEFINITIONS_DIR", root)
	if _, _, err := selectTools(toolcatalog.Catalog{}); err == nil {
		t.Fatal("expected shell definition to be rejected")
	}
}

func TestSelectToolsByRepositoryFamily(t *testing.T) {
	root := t.TempDir()
	definitions := []string{
		"name: nmap\ncommand: nmap\nenabled: true\ndeployment:\n  family: recon\n  runtime: family\ndescription: recon\n",
		"name: sqlmap\ncommand: sqlmap\nenabled: true\ndeployment:\n  family: web\n  runtime: family\ndescription: web\n",
	}
	for _, content := range definitions {
		name := strings.SplitN(strings.SplitN(content, "\n", 2)[0], ": ", 2)[1]
		if err := os.WriteFile(filepath.Join(root, name+".yaml"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("TOOL_FAMILY", "recon")
	t.Setenv("TOOL_DEFINITIONS_DIR", root)
	selected, generic, err := selectTools(toolcatalog.Catalog{})
	if err != nil {
		t.Fatal(err)
	}
	if !generic || len(selected) != 1 || selected[0].tool.ID != "nmap" {
		t.Fatalf("unexpected family selection: generic=%v selected=%#v", generic, selected)
	}
}

func TestPreflightGenericTools(t *testing.T) {
	definition := &config.ToolConfig{Name: "printf-tool", Command: "printf", Enabled: true}
	selected := []selectedTool{{definition: definition}}
	if err := preflightGenericTools(selected); err != nil {
		t.Fatalf("expected printf to be available: %v", err)
	}
	missing := &config.ToolConfig{Name: "missing", Command: "cyberstrike-command-not-installed", Enabled: true}
	if err := preflightGenericTools([]selectedTool{{definition: missing}}); err == nil {
		t.Fatal("expected missing generic command to fail preflight")
	}
}

func TestRunGenericUsesSharedArgumentMapping(t *testing.T) {
	position := 1
	reports := t.TempDir()
	definition := &config.ToolConfig{
		Name:        "printf-tool",
		Command:     "printf",
		Enabled:     true,
		Description: "test",
		Parameters: []config.ParameterConfig{
			{Name: "target", Type: "string", Required: true, Position: &position, Format: "positional"},
		},
	}
	svc := &service{
		tool:       genericCatalogTool(*definition),
		definition: definition,
		generic:    true,
		reportsDir: reports,
		semaphore:  make(chan struct{}, 1),
	}
	result, err := svc.runGeneric(context.Background(), map[string]interface{}{"target": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || len(result.Content) != 1 {
		t.Fatalf("unexpected result: %#v", result)
	}
	var metadata runResult
	if err := json.Unmarshal([]byte(result.Content[0].Text), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Status != "completed" || metadata.StdoutTail != "hello" {
		t.Fatalf("unexpected generic execution metadata: %#v", metadata)
	}
	if metadata.TextReport == "" || len(metadata.ReportFiles) != 2 || len(metadata.ReportSHA256) != 2 {
		t.Fatalf("generic execution did not persist bounded reports: %#v", metadata)
	}
	if _, err := os.Stat(filepath.Join(reports, metadata.TextReport)); err != nil {
		t.Fatalf("stdout report missing: %v", err)
	}
}
