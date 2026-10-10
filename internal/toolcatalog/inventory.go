package toolcatalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"cyberstrike-ai/internal/config"

	"gopkg.in/yaml.v3"
)

// ToolInventory is the compatibility inventory for the repository's
// agent-facing tools/*.yaml definitions.  The deployment catalog describes
// isolated services; the inventory describes every tool the current
// CyberStrikeAI project knows about so a deployment cannot silently drop a
// capability when it moves tools out of the control image.
type ToolInventory struct {
	SchemaVersion int             `yaml:"schema_version"`
	Source        string          `yaml:"source"`
	Total         int             `yaml:"total"`
	Enabled       int             `yaml:"enabled"`
	Disabled      int             `yaml:"disabled"`
	Tools         []InventoryTool `yaml:"tools"`
}

type InventoryTool struct {
	ID             string `yaml:"id"`
	Source         string `yaml:"source"`
	Command        string `yaml:"command"`
	Enabled        bool   `yaml:"enabled"`
	Layer          string `yaml:"layer"`   // control | tool-plane
	Family         string `yaml:"family"`  // logical capability group
	Runtime        string `yaml:"runtime"` // builtin | native | external-adapter
	Containerized  bool   `yaml:"containerized"`
	Adapter        string `yaml:"adapter,omitempty"` // deployment catalog id, if one exists
	RuntimeImage   string `yaml:"runtime_image,omitempty"`
	RuntimeService string `yaml:"runtime_service,omitempty"`
	Availability   string `yaml:"availability"`  // enabled | disabled | builtin
	BinaryStatus   string `yaml:"binary_status"` // unverified | runtime-declared | adapter-ready | builtin | disabled
	Migration      string `yaml:"migration"`     // preserved | runtime-declared | adapter-ready | pending
	Notes          string `yaml:"notes,omitempty"`
}

// RuntimeManifest describes the family images that are built from the
// repository's tool definitions. It is intentionally separate from the MCP
// catalog: the catalog contains fixed adapters such as Nuclei/ZAP, while this
// manifest maps the remaining compatible YAML tools to independently built
// family runtimes.
type RuntimeManifest struct {
	SchemaVersion int                      `yaml:"schema_version"`
	Name          string                   `yaml:"name,omitempty"`
	BaseImage     string                   `yaml:"base_image"`
	Families      map[string]RuntimeFamily `yaml:"families"`
}

type RuntimeFamily struct {
	Service        string `yaml:"service"`
	ComposeProfile string `yaml:"compose_profile"`
	Image          string `yaml:"image"`
}

func LoadRuntimeManifest(path string) (RuntimeManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RuntimeManifest{}, fmt.Errorf("read runtime manifest %q: %w", path, err)
	}
	var manifest RuntimeManifest
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&manifest); err != nil {
		return RuntimeManifest{}, fmt.Errorf("decode runtime manifest %q: %w", path, err)
	}
	if manifest.SchemaVersion != 1 {
		return RuntimeManifest{}, fmt.Errorf("runtime manifest schema_version must be 1, got %d", manifest.SchemaVersion)
	}
	if strings.TrimSpace(manifest.BaseImage) == "" {
		return RuntimeManifest{}, errors.New("runtime manifest base_image is required")
	}
	if len(manifest.Families) == 0 {
		return RuntimeManifest{}, errors.New("runtime manifest families must not be empty")
	}
	for family, entry := range manifest.Families {
		if strings.TrimSpace(family) == "" || strings.TrimSpace(entry.Service) == "" || strings.TrimSpace(entry.ComposeProfile) == "" || strings.TrimSpace(entry.Image) == "" {
			return RuntimeManifest{}, fmt.Errorf("runtime family %q must declare service, compose_profile and image", family)
		}
	}
	return manifest, nil
}

// BuildInventory reads the same directory the application loads at runtime.
// Without a runtime manifest it deliberately does not infer binaries from a
// third-party image. BuildInventoryWithRuntime adds only the explicit family
// images declared by the repository.
func BuildInventory(toolsDir string, catalog Catalog) (ToolInventory, error) {
	return BuildInventoryWithRuntime(toolsDir, catalog, "")
}

// BuildInventoryWithRuntime is BuildInventory plus the family image mapping.
// An empty runtimeManifestPath preserves the pre-runtime behavior used by
// unit tests and by callers that only need the historical adapter catalog.
func BuildInventoryWithRuntime(toolsDir string, catalog Catalog, runtimeManifestPath string) (ToolInventory, error) {
	var runtimeManifest RuntimeManifest
	var err error
	if strings.TrimSpace(runtimeManifestPath) != "" {
		runtimeManifest, err = LoadRuntimeManifest(runtimeManifestPath)
		if err != nil {
			return ToolInventory{}, err
		}
	}
	entries, err := os.ReadDir(toolsDir)
	if err != nil {
		return ToolInventory{}, fmt.Errorf("read tool definitions: %w", err)
	}
	type sourceTool struct {
		tool   config.ToolConfig
		source string
	}
	loaded := make([]sourceTool, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || (filepath.Ext(entry.Name()) != ".yaml" && filepath.Ext(entry.Name()) != ".yml") {
			continue
		}
		path := filepath.Join(toolsDir, entry.Name())
		tool, loadErr := config.LoadToolFromFile(path)
		if loadErr != nil {
			return ToolInventory{}, fmt.Errorf("load %s: %w", path, loadErr)
		}
		loaded = append(loaded, sourceTool{tool: *tool, source: filepath.ToSlash(filepath.Join(filepath.Base(toolsDir), entry.Name()))})
	}
	if len(loaded) == 0 {
		return ToolInventory{}, fmt.Errorf("no tool definitions found in %q", toolsDir)
	}

	adapterBySource := map[string]string{
		"nuclei": "nuclei_scan",
		"zap":    "zap_baseline",
	}
	inventoryTools := make([]InventoryTool, 0, len(loaded))
	for _, loadedTool := range loaded {
		tool := loadedTool.tool
		entry := InventoryTool{
			ID:           tool.Name,
			Source:       loadedTool.source,
			Command:      tool.Command,
			Enabled:      tool.Enabled,
			Availability: "disabled",
			BinaryStatus: "disabled",
			Migration:    "preserved",
			Layer:        "control",
			Runtime:      "native",
			Family:       ClassifyToolFamily(tool.Name, tool.Command),
		}
		if tool.Enabled {
			entry.Availability = "enabled"
			entry.BinaryStatus = "unverified"
		}
		if strings.HasPrefix(strings.TrimSpace(tool.Command), "internal:") {
			entry.Layer = "control"
			entry.Runtime = "builtin"
			entry.Availability = "builtin"
			entry.BinaryStatus = "builtin"
			entry.Family = "control"
			entry.Migration = "preserved"
			entry.Notes = "由 CyberStrikeAI 控制平面内部实现，不能迁移为外部命令容器。"
		}
		if adapter := adapterBySource[tool.Name]; adapter != "" {
			if adapterTool, ok := catalog.Tool(adapter); ok && adapterTool.Enabled {
				entry.Layer = "tool-plane"
				entry.Runtime = "external-adapter"
				entry.Containerized = true
				entry.Adapter = adapter
				entry.Migration = "adapter-ready"
				if tool.Enabled {
					entry.BinaryStatus = "adapter-ready"
				}
				entry.Notes = "已有独立工具平面适配器；工具 YAML 仍保留，保证原有名称和参数兼容。"
			}
		}
		if !entry.Containerized && entry.Enabled && entry.Runtime != "builtin" && len(runtimeManifest.Families) > 0 {
			if runtime, ok := runtimeManifest.Families[entry.Family]; ok {
				entry.Layer = "tool-plane"
				entry.Runtime = "family"
				entry.Containerized = true
				entry.RuntimeImage = runtime.Image
				entry.RuntimeService = runtime.Service
				entry.BinaryStatus = "runtime-declared"
				entry.Migration = "runtime-declared"
				entry.Notes = "由独立工具族运行时提供；镜像构建阶段使用 toolcatalog doctor 逐项验收命令。"
			}
		}
		inventoryTools = append(inventoryTools, entry)
	}
	sort.Slice(inventoryTools, func(i, j int) bool { return inventoryTools[i].ID < inventoryTools[j].ID })

	inventory := ToolInventory{
		SchemaVersion: 1,
		Source:        filepath.ToSlash(filepath.Clean(toolsDir)),
		Total:         len(inventoryTools),
		Tools:         inventoryTools,
	}
	for _, entry := range inventoryTools {
		if entry.Enabled {
			inventory.Enabled++
		} else {
			inventory.Disabled++
		}
	}
	return inventory, nil
}

// MarshalInventory emits a stable YAML document so CI can diff generated
// inventory changes when tools/*.yaml is added, removed or disabled.
func MarshalInventory(inventory ToolInventory) ([]byte, error) {
	return yaml.Marshal(inventory)
}

// ClassifyToolFamily maps the repository's stable tool IDs to the deployment
// families used by the compatibility adapter. The mapping is based on the
// existing YAML names rather than image contents, so a family can be selected
// before its runtime image is built and verified.
func ClassifyToolFamily(id, command string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	command = strings.ToLower(strings.TrimSpace(command))
	switch {
	case id == "exec" || id == "execute-python-script" || id == "install-python-package" || strings.HasPrefix(command, "internal:"):
		return "control"
	case containsAny(id, "cloud", "checkov", "prowler", "scout", "pacu", "terrascan", "trivy", "kube", "falco", "clair"):
		return "cloud"
	case containsAny(id, "bloodhound", "enum4linux", "impacket", "netexec", "rpcclient", "smbmap", "responder", "hydra"):
		return "identity"
	case containsAny(id, "linpeas", "metasploit", "msfvenom"):
		return "exploitation"
	case containsAny(id, "binwalk", "checksec", "gdb", "ghidra", "objdump", "one-gadget", "pwntools", "pwninit", "radare", "rop", "steghide", "strings", "volatility", "xxd", "zsteg", "hash", "john", "angr", "foremost", "exiftool"):
		return "binary-forensics"
	case containsAny(id, "sqlmap", "nikto", "ffuf", "gobuster", "ferox", "dirsearch", "dalfox", "jaeles", "nuclei", "wafw00f", "xsser", "arjun", "paramspider", "graphql", "wpscan", "x8", "zap", "dotdotpwn", "jwt", "http-framework"):
		return "web"
	case containsAny(id, "fofa", "quake", "shodan", "virustotal", "zoomeye", "dnslog"):
		return "external-intel"
	case containsAny(id, "amass", "arp", "dnsenum", "fierce", "gau", "katana", "masscan", "nbtscan", "nmap", "rustscan", "subfinder", "wayback", "fscan"):
		return "recon"
	default:
		return "utility"
	}
}

func containsAny(value string, words ...string) bool {
	for _, word := range words {
		if strings.Contains(value, word) {
			return true
		}
	}
	return false
}
