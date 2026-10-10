// Package toolcatalog defines the deployment contract for the external tool
// plane.  The catalog is deliberately separate from tools/*.yaml: those files
// describe the agent-facing command schema, while this package describes the
// image, isolation, target and report contract used by Compose and a future
// runner.
package toolcatalog

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var (
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	servicePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)
	digestPattern  = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)
)

// Catalog is the versioned source of truth for deploy/tools/docker-compose.yml.
// Compose remains the execution description; this file carries the policy
// metadata that Compose cannot express (risk, target scope and approval).
type Catalog struct {
	SchemaVersion int                `yaml:"schema_version"`
	Name          string             `yaml:"name,omitempty"`
	Inventory     string             `yaml:"inventory,omitempty"`
	Profiles      map[string]Profile `yaml:"profiles,omitempty"`
	Tools         []Tool             `yaml:"tools"`
}

type Profile struct {
	Description string `yaml:"description,omitempty"`
}

type Tool struct {
	ID             string     `yaml:"id"`
	Service        string     `yaml:"service"`
	Kind           string     `yaml:"kind"`      // job | mcp
	Transport      string     `yaml:"transport"` // compose | http | sse
	Image          string     `yaml:"image"`
	Profile        string     `yaml:"profile"`
	ComposeProfile string     `yaml:"compose_profile,omitempty"`
	Risk           string     `yaml:"risk"`     // low | medium | high | critical
	Approval       string     `yaml:"approval"` // none | human
	Enabled        bool       `yaml:"enabled"`
	Target         TargetSpec `yaml:"target"`
	Limits         LimitSpec  `yaml:"limits"`
	Output         OutputSpec `yaml:"output"`
	Capabilities   []string   `yaml:"capabilities,omitempty"`
	ResultAdapter  string     `yaml:"result_adapter,omitempty"`
	Endpoint       string     `yaml:"endpoint,omitempty"`
	Description    string     `yaml:"description,omitempty"`
}

type TargetSpec struct {
	Input             string `yaml:"input" json:"input"` // url_file | single_url | host_file
	AllowlistRequired bool   `yaml:"allowlist_required" json:"allowlist_required"`
	SupportsMultiple  bool   `yaml:"supports_multiple" json:"supports_multiple"`
}

type LimitSpec struct {
	TimeoutSeconds int `yaml:"timeout_seconds" json:"timeout_seconds"`
	Rate           int `yaml:"rate,omitempty" json:"rate,omitempty"`
	Concurrency    int `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
	MaxTargets     int `yaml:"max_targets,omitempty" json:"max_targets,omitempty"`
}

type OutputSpec struct {
	Format        string `yaml:"format" json:"format"`
	Directory     string `yaml:"directory" json:"directory"`
	ReportPattern string `yaml:"report_pattern" json:"report_pattern"`
}

type ValidateOptions struct {
	// RequireDigests is intended for CI/production. Development catalogs may
	// use a pinned version tag while the operator resolves and records the
	// digest before deployment.
	RequireDigests bool
}

// Load reads and strictly decodes a catalog file.
func Load(path string) (Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Catalog{}, fmt.Errorf("read catalog %q: %w", path, err)
	}
	var catalog Catalog
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("decode catalog %q: %w", path, err)
	}
	return catalog, nil
}

// Validate checks the contract shared by the catalog, Compose services and a
// runner. It intentionally rejects floating image tags such as latest.
func (c Catalog) Validate(opts ValidateOptions) error {
	if c.SchemaVersion != 1 {
		return fmt.Errorf("schema_version must be 1, got %d", c.SchemaVersion)
	}
	if len(c.Tools) == 0 {
		return errors.New("tools must contain at least one entry")
	}
	if len(c.Profiles) == 0 {
		return errors.New("profiles must contain at least one entry")
	}

	seenIDs := make(map[string]struct{}, len(c.Tools))
	seenServices := make(map[string]struct{}, len(c.Tools))
	for i, tool := range c.Tools {
		prefix := fmt.Sprintf("tools[%d]", i)
		if !idPattern.MatchString(strings.TrimSpace(tool.ID)) {
			return fmt.Errorf("%s.id must match %s", prefix, idPattern.String())
		}
		if _, ok := seenIDs[tool.ID]; ok {
			return fmt.Errorf("duplicate tool id %q", tool.ID)
		}
		seenIDs[tool.ID] = struct{}{}
		if !servicePattern.MatchString(strings.TrimSpace(tool.Service)) {
			return fmt.Errorf("%s.service must match %s", prefix, servicePattern.String())
		}
		if _, ok := seenServices[tool.Service]; ok {
			return fmt.Errorf("duplicate compose service %q", tool.Service)
		}
		seenServices[tool.Service] = struct{}{}
		if tool.Kind != "job" && tool.Kind != "mcp" {
			return fmt.Errorf("%s.kind must be job or mcp", prefix)
		}
		if tool.Profile == "" {
			return fmt.Errorf("%s.profile is required", prefix)
		}
		if _, ok := c.Profiles[tool.Profile]; !ok {
			return fmt.Errorf("%s.profile %q is not declared", prefix, tool.Profile)
		}
		if tool.Risk != "low" && tool.Risk != "medium" && tool.Risk != "high" && tool.Risk != "critical" {
			return fmt.Errorf("%s.risk must be low, medium, high or critical", prefix)
		}
		if tool.Approval != "none" && tool.Approval != "human" {
			return fmt.Errorf("%s.approval must be none or human", prefix)
		}
		if (tool.Risk == "high" || tool.Risk == "critical") && tool.Approval != "human" {
			return fmt.Errorf("%s high-risk tools require approval: human", prefix)
		}
		if strings.TrimSpace(tool.Image) == "" {
			return fmt.Errorf("%s.image is required", prefix)
		}
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(tool.Image)), ":latest") {
			return fmt.Errorf("%s.image must not use latest", prefix)
		}
		if opts.RequireDigests && !digestPattern.MatchString(strings.TrimSpace(tool.Image)) {
			return fmt.Errorf("%s.image must include a sha256 digest in production", prefix)
		}
		if tool.Kind == "job" && tool.Transport != "compose" {
			return fmt.Errorf("%s job transport must be compose", prefix)
		}
		if tool.Kind == "mcp" && tool.Transport != "http" && tool.Transport != "sse" {
			return fmt.Errorf("%s mcp transport must be http or sse", prefix)
		}
		if tool.Kind == "mcp" && strings.TrimSpace(tool.Endpoint) == "" {
			return fmt.Errorf("%s.endpoint is required for mcp tools", prefix)
		}
		if tool.Target.Input != "url_file" && tool.Target.Input != "single_url" && tool.Target.Input != "host_file" {
			return fmt.Errorf("%s.target.input must be url_file, single_url or host_file", prefix)
		}
		if !tool.Target.AllowlistRequired {
			return fmt.Errorf("%s.target.allowlist_required must be true", prefix)
		}
		if tool.Limits.TimeoutSeconds <= 0 {
			return fmt.Errorf("%s.limits.timeout_seconds must be positive", prefix)
		}
		if tool.Limits.MaxTargets < 0 || tool.Limits.Rate < 0 || tool.Limits.Concurrency < 0 {
			return fmt.Errorf("%s.limits values cannot be negative", prefix)
		}
		if strings.TrimSpace(tool.Output.Format) == "" || strings.TrimSpace(tool.Output.Directory) == "" || strings.TrimSpace(tool.Output.ReportPattern) == "" {
			return fmt.Errorf("%s.output.format, directory and report_pattern are required", prefix)
		}
	}
	return nil
}

// ValidateCompose checks that every catalog service exists in the Compose
// file and carries the profile declared by the catalog. It intentionally does
// not try to fully validate the Compose specification; Docker Compose remains
// responsible for that part.
func (c Catalog) ValidateCompose(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read compose file %q: %w", path, err)
	}
	var document struct {
		Services map[string]struct {
			Profiles []string `yaml:"profiles"`
		} `yaml:"services"`
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("decode compose file %q: %w", path, err)
	}
	for _, tool := range c.Tools {
		service, ok := document.Services[tool.Service]
		if !ok {
			return fmt.Errorf("catalog tool %q references missing compose service %q", tool.ID, tool.Service)
		}
		composeProfile := tool.ComposeProfile
		if composeProfile == "" {
			composeProfile = tool.Profile
		}
		foundProfile := false
		for _, profile := range service.Profiles {
			if profile == composeProfile {
				foundProfile = true
				break
			}
		}
		if !foundProfile {
			return fmt.Errorf("compose service %q must declare profile %q for tool %q", tool.Service, composeProfile, tool.ID)
		}
	}
	return nil
}

// Tool returns a catalog entry by stable id.
func (c Catalog) Tool(id string) (Tool, bool) {
	for _, tool := range c.Tools {
		if tool.ID == id {
			return tool, true
		}
	}
	return Tool{}, false
}
