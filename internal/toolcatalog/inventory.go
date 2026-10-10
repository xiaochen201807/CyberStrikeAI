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
// agent-facing tools/*.yaml definitions. The deployment catalog describes
// isolated services; the inventory describes every YAML definition the current
// CyberStrikeAI checkout knows about so a deployment cannot silently drop a
// capability when it moves tools out of the control image.
//
// CountScope and OfficialToolSurface are deliberately emitted alongside the
// numeric counts. The upstream README uses the broader product wording
// "100+ curated tools", while Total/Enabled/Disabled are exact counts of the
// YAML definitions in this checkout and must not be presented as the full
// runtime tool surface.
type ToolInventory struct {
	SchemaVersion       int             `yaml:"schema_version"`
	Source              string          `yaml:"source"`
	CountScope          string          `yaml:"count_scope"`
	OfficialToolSurface string          `yaml:"official_tool_surface"`
	Total               int             `yaml:"total"`
	Enabled             int             `yaml:"enabled"`
	Disabled            int             `yaml:"disabled"`
	Tools               []InventoryTool `yaml:"tools"`
}

type InventoryTool struct {
	ID             string `yaml:"id"`
	Source         string `yaml:"source"`
	Command        string `yaml:"command"`
	Enabled        bool   `yaml:"enabled"`
	Layer          string `yaml:"layer"`   // control | tool-plane
	Family         string `yaml:"family"`  // logical capability group
	Runtime        string `yaml:"runtime"` // family | external-adapter | control-image | builtin | disabled
	Containerized  bool   `yaml:"containerized"`
	Adapter        string `yaml:"adapter,omitempty"` // deployment catalog id, if one exists
	RuntimeImage   string `yaml:"runtime_image,omitempty"`
	RuntimeService string `yaml:"runtime_service,omitempty"`
	Availability   string `yaml:"availability"`  // enabled | disabled | builtin
	BinaryStatus   string `yaml:"binary_status"` // control-image | unverified | runtime-declared | adapter-ready | builtin | disabled
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

const (
	DeploymentRuntimeFamily          = "family"
	DeploymentRuntimeExternalAdapter = "external-adapter"
	DeploymentRuntimeControlImage    = "control-image"
	DeploymentRuntimeBuiltin         = "builtin"
	DeploymentRuntimeDisabled        = "disabled"
)

var deploymentFamilies = map[string]struct{}{
	"control":          {},
	"recon":            {},
	"web":              {},
	"identity":         {},
	"binary-forensics": {},
	"cloud":            {},
	"exploitation":     {},
	"external-intel":   {},
	"utility":          {},
}

// ResolveToolDeployment validates and returns the explicit deployment
// metadata carried by a tools/*.yaml definition.  The tool plane must not
// infer a family from a name: a new or renamed tool has to declare its
// deployment contract before it can be selected by a runtime image.
func ResolveToolDeployment(tool config.ToolConfig) (config.ToolDeploymentConfig, error) {
	if tool.Deployment == nil {
		return config.ToolDeploymentConfig{}, fmt.Errorf("tool %q is missing deployment metadata", tool.Name)
	}
	deployment := *tool.Deployment
	deployment.Family = strings.ToLower(strings.TrimSpace(deployment.Family))
	deployment.Runtime = strings.ToLower(strings.TrimSpace(deployment.Runtime))
	deployment.Adapter = strings.TrimSpace(deployment.Adapter)
	if _, ok := deploymentFamilies[deployment.Family]; !ok {
		return config.ToolDeploymentConfig{}, fmt.Errorf("tool %q declares unsupported deployment family %q", tool.Name, deployment.Family)
	}
	switch deployment.Runtime {
	case DeploymentRuntimeFamily, DeploymentRuntimeExternalAdapter, DeploymentRuntimeControlImage, DeploymentRuntimeBuiltin, DeploymentRuntimeDisabled:
	default:
		return config.ToolDeploymentConfig{}, fmt.Errorf("tool %q declares unsupported deployment runtime %q", tool.Name, deployment.Runtime)
	}
	if deployment.Runtime == DeploymentRuntimeExternalAdapter && deployment.Adapter == "" {
		return config.ToolDeploymentConfig{}, fmt.Errorf("tool %q uses external-adapter runtime without adapter", tool.Name)
	}
	if deployment.Runtime != DeploymentRuntimeExternalAdapter && deployment.Adapter != "" {
		return config.ToolDeploymentConfig{}, fmt.Errorf("tool %q declares adapter %q for runtime %q", tool.Name, deployment.Adapter, deployment.Runtime)
	}
	if tool.Enabled && deployment.Runtime == DeploymentRuntimeDisabled {
		return config.ToolDeploymentConfig{}, fmt.Errorf("enabled tool %q cannot use disabled deployment runtime", tool.Name)
	}
	if deployment.Family == "control" && deployment.Runtime == DeploymentRuntimeFamily {
		return config.ToolDeploymentConfig{}, fmt.Errorf("control-plane tool %q cannot use family runtime", tool.Name)
	}
	if deployment.Family != "control" && (deployment.Runtime == DeploymentRuntimeControlImage || deployment.Runtime == DeploymentRuntimeBuiltin) {
		return config.ToolDeploymentConfig{}, fmt.Errorf("non-control tool %q cannot use %q runtime", tool.Name, deployment.Runtime)
	}
	if strings.HasPrefix(strings.TrimSpace(tool.Command), "internal:") && deployment.Runtime != DeploymentRuntimeBuiltin {
		return config.ToolDeploymentConfig{}, fmt.Errorf("internal tool %q must use builtin deployment runtime", tool.Name)
	}
	if deployment.Runtime == DeploymentRuntimeBuiltin && !strings.HasPrefix(strings.TrimSpace(tool.Command), "internal:") {
		return config.ToolDeploymentConfig{}, fmt.Errorf("builtin tool %q must use an internal command", tool.Name)
	}
	return deployment, nil
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

	inventoryTools := make([]InventoryTool, 0, len(loaded))
	for _, loadedTool := range loaded {
		tool := loadedTool.tool
		deployment, deploymentErr := ResolveToolDeployment(tool)
		if deploymentErr != nil {
			return ToolInventory{}, fmt.Errorf("load deployment metadata for %s: %w", loadedTool.source, deploymentErr)
		}
		entry := InventoryTool{
			ID:           tool.Name,
			Source:       loadedTool.source,
			Command:      tool.Command,
			Enabled:      tool.Enabled,
			Availability: "disabled",
			BinaryStatus: "disabled",
			Migration:    "preserved",
			Layer:        "control",
			Runtime:      deployment.Runtime,
			Family:       deployment.Family,
		}
		if tool.Enabled {
			entry.Availability = "enabled"
			entry.BinaryStatus = "unverified"
		}
		if deployment.Runtime == DeploymentRuntimeBuiltin {
			entry.Layer = "control"
			entry.Runtime = "builtin"
			entry.Availability = "builtin"
			entry.BinaryStatus = "builtin"
			entry.Family = "control"
			entry.Migration = "preserved"
			entry.Notes = "由 CyberStrikeAI 控制平面内部实现，不能迁移为外部命令容器。"
		}
		if adapter := deployment.Adapter; adapter != "" {
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
			} else {
				return ToolInventory{}, fmt.Errorf("tool %q references missing or disabled adapter %q", tool.Name, adapter)
			}
		}
		// Shell/Python helpers are intentionally control-plane capabilities, not
		// scanner-family commands.  The control Dockerfile installs and checks
		// their interpreters, so record that executable runtime explicitly.
		if entry.Enabled && deployment.Runtime == DeploymentRuntimeControlImage {
			entry.Containerized = true
			entry.RuntimeImage = "cyberstrike-ai:control"
			entry.RuntimeService = "cyberstrike-ai"
			entry.BinaryStatus = "control-image"
			entry.Migration = "preserved"
			entry.Notes = "由控制平面镜像提供 sh、bash、python3 和 venv 运行时；不迁移到扫描器族镜像。"
		}
		if !entry.Containerized && entry.Enabled && deployment.Runtime == DeploymentRuntimeFamily && len(runtimeManifest.Families) > 0 {
			if runtime, ok := runtimeManifest.Families[entry.Family]; ok {
				entry.Layer = "tool-plane"
				entry.Runtime = "family"
				entry.Containerized = true
				entry.RuntimeImage = runtime.Image
				entry.RuntimeService = runtime.Service
				entry.BinaryStatus = "runtime-declared"
				entry.Migration = "runtime-declared"
				entry.Notes = "由独立工具族运行时提供；镜像构建阶段使用 toolcatalog doctor 逐项验收命令。"
			} else {
				return ToolInventory{}, fmt.Errorf("tool %q declares family runtime %q without a runtime manifest entry", tool.Name, entry.Family)
			}
		}
		inventoryTools = append(inventoryTools, entry)
	}
	sort.Slice(inventoryTools, func(i, j int) bool { return inventoryTools[i].ID < inventoryTools[j].ID })

	inventory := ToolInventory{
		SchemaVersion:       1,
		Source:              filepath.ToSlash(filepath.Clean(toolsDir)),
		CountScope:          "tools/*.yaml definitions",
		OfficialToolSurface: "100+",
		Total:               len(inventoryTools),
		Tools:               inventoryTools,
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
