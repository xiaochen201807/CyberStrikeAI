package toolcatalog

import (
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
	ID            string `yaml:"id"`
	Source        string `yaml:"source"`
	Command       string `yaml:"command"`
	Enabled       bool   `yaml:"enabled"`
	Layer         string `yaml:"layer"`   // control | tool-plane
	Family        string `yaml:"family"`  // logical capability group
	Runtime       string `yaml:"runtime"` // builtin | native | external-adapter
	Containerized bool   `yaml:"containerized"`
	Adapter       string `yaml:"adapter,omitempty"` // deployment catalog id, if one exists
	Availability  string `yaml:"availability"`      // enabled | disabled | builtin
	BinaryStatus  string `yaml:"binary_status"`     // unverified | adapter-ready | builtin | disabled
	Migration     string `yaml:"migration"`         // preserved | adapter-ready | pending
	Notes         string `yaml:"notes,omitempty"`
}

// BuildInventory reads the same directory the application loads at runtime.
// It deliberately does not infer binaries from a third-party image: a tool is
// only marked containerized when an explicit adapter exists in the deployment
// catalog.
func BuildInventory(toolsDir string, catalog Catalog) (ToolInventory, error) {
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
