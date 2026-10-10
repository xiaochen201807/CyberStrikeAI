package toolcatalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildInventoryPreservesAllToolDefinitions(t *testing.T) {
	root := t.TempDir()
	toolsDir := filepath.Join(root, "tools")
	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		t.Fatal(err)
	}
	write := func(name, command string, enabled bool) {
		t.Helper()
		content := "name: " + name + "\ncommand: " + command + "\nenabled: "
		if enabled {
			content += "true\n"
		} else {
			content += "false\n"
		}
		content += "description: test\n"
		if err := os.WriteFile(filepath.Join(toolsDir, name+".yaml"), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha", "alpha", true)
	write("query_execution_result", "internal:query_execution_result", true)
	write("disabled", "disabled", false)

	catalog := Catalog{
		Tools: []Tool{
			{ID: "nuclei_scan", Enabled: true},
			{ID: "zap_baseline", Enabled: true},
		},
	}
	inv, err := BuildInventory(toolsDir, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Total != 3 || inv.Enabled != 2 || inv.Disabled != 1 {
		t.Fatalf("unexpected counts: %#v", inv)
	}
	if inv.CountScope != "tools/*.yaml definitions" || inv.OfficialToolSurface != "100+" {
		t.Fatalf("unexpected inventory scope metadata: %#v", inv)
	}
	byID := make(map[string]InventoryTool, len(inv.Tools))
	for _, entry := range inv.Tools {
		byID[entry.ID] = entry
	}
	if byID["query_execution_result"].Runtime != "builtin" {
		t.Fatalf("internal tool was not classified as builtin: %#v", byID["query_execution_result"])
	}
	if byID["query_execution_result"].BinaryStatus != "builtin" {
		t.Fatalf("internal tool binary status = %q, want builtin", byID["query_execution_result"].BinaryStatus)
	}
	if byID["alpha"].Migration != "preserved" || byID["alpha"].Containerized {
		t.Fatalf("native tool was incorrectly marked containerized: %#v", byID["alpha"])
	}
	if byID["alpha"].BinaryStatus != "unverified" {
		t.Fatalf("native tool binary status = %q, want unverified", byID["alpha"].BinaryStatus)
	}
	if byID["alpha"].Source != "tools/alpha.yaml" {
		t.Fatalf("unexpected source path: %q", byID["alpha"].Source)
	}
}

func TestCheckRuntimeReportsBuiltinAndMissing(t *testing.T) {
	root := t.TempDir()
	content := `name: builtin
command: internal:test
enabled: true
description: builtin
`
	if err := os.WriteFile(filepath.Join(root, "builtin.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	content = `name: missing
command: command-that-does-not-exist-cyberstrike
enabled: true
description: missing
`
	if err := os.WriteFile(filepath.Join(root, "missing.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	checks, err := CheckRuntime(root, false)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, check := range checks {
		statuses[check.ID] = check.Status
	}
	if statuses["builtin"] != "builtin" || statuses["missing"] != "missing" {
		t.Fatalf("unexpected runtime statuses: %#v", statuses)
	}
}

func TestCheckRuntimeFamilyFiltersDefinitions(t *testing.T) {
	root := t.TempDir()
	content := `name: nmap
command: nmap
enabled: true
description: recon
`
	if err := os.WriteFile(filepath.Join(root, "nmap.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	content = `name: checkov
command: checkov
enabled: true
description: cloud
`
	if err := os.WriteFile(filepath.Join(root, "checkov.yaml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	checks, err := CheckRuntimeFamily(root, false, "recon")
	if err != nil {
		t.Fatal(err)
	}
	if len(checks) != 1 || checks[0].ID != "nmap" {
		t.Fatalf("unexpected family checks: %#v", checks)
	}
}
