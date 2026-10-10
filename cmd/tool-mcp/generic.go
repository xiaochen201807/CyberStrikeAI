package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/toolcatalog"
	"cyberstrike-ai/internal/toolrunner"

	"github.com/google/uuid"
)

type selectedTool struct {
	tool       toolcatalog.Tool
	definition *config.ToolConfig
	generic    bool
}

// selectTools preserves the original one-tool-per-image mode and adds an
// opt-in compatibility mode for a family runtime image.  The compatibility
// mode reads the real tools/*.yaml definitions, so moving a tool behind MCP
// does not create a second, drifting schema.
func selectTools(catalog toolcatalog.Catalog) ([]selectedTool, bool, error) {
	ids := splitIDs(os.Getenv("TOOL_IDS"))
	family := strings.TrimSpace(os.Getenv("TOOL_FAMILY"))
	if len(ids) > 0 && family != "" {
		return nil, true, errors.New("TOOL_IDS and TOOL_FAMILY are mutually exclusive")
	}
	if len(ids) == 0 && family == "" {
		id := strings.TrimSpace(os.Getenv("TOOL_ID"))
		if id == "" {
			return nil, false, errors.New("TOOL_ID, TOOL_IDS or TOOL_FAMILY must be set")
		}
		tool, ok := catalog.Tool(id)
		if !ok || !tool.Enabled {
			return nil, false, fmt.Errorf("enabled catalog tool not found: %q", id)
		}
		if tool.Kind != "mcp" {
			return nil, false, fmt.Errorf("tool %q is not an MCP tool", id)
		}
		if err := validateToolAdapter(tool); err != nil {
			return nil, false, err
		}
		return []selectedTool{{tool: tool}}, false, nil
	}

	dir := strings.TrimSpace(envOr("TOOL_DEFINITIONS_DIR", "/etc/cyberstrike/tools"))
	definitions, err := config.LoadToolsFromDir(dir)
	if err != nil {
		return nil, true, fmt.Errorf("load generic tool definitions: %w", err)
	}
	byID := make(map[string]config.ToolConfig, len(definitions))
	for _, definition := range definitions {
		byID[definition.Name] = definition
	}
	if family != "" {
		family = strings.ToLower(family)
		for _, definition := range definitions {
			if definition.Enabled && toolcatalog.ClassifyToolFamily(definition.Name, definition.Command) == family {
				ids = append(ids, definition.Name)
			}
		}
		sort.Strings(ids)
		if len(ids) == 0 {
			return nil, true, fmt.Errorf("no enabled repository tools belong to TOOL_FAMILY=%q", family)
		}
	}
	selected := make([]selectedTool, 0, len(ids))
	for _, id := range ids {
		definition, ok := byID[id]
		if !ok {
			return nil, true, fmt.Errorf("generic tool definition not found: %q", id)
		}
		if !definition.Enabled {
			return nil, true, fmt.Errorf("generic tool %q is disabled in tools/*.yaml", id)
		}
		if strings.HasPrefix(strings.TrimSpace(definition.Command), "internal:") {
			return nil, true, fmt.Errorf("generic tool %q is an internal control-plane tool", id)
		}
		if isUnsafeGenericCommand(definition.Command) {
			return nil, true, fmt.Errorf("generic tool %q uses a control-plane shell command; keep it in the control plane", id)
		}
		selected = append(selected, selectedTool{
			tool:       genericCatalogTool(definition),
			definition: &definition,
			generic:    true,
		})
	}
	return selected, true, nil
}

func genericCatalogTool(definition config.ToolConfig) toolcatalog.Tool {
	return toolcatalog.Tool{
		ID:             definition.Name,
		Service:        envOr("TOOL_MCP_SERVICE", "tool-compat-mcp"),
		Kind:           "mcp",
		Transport:      "sse",
		Image:          strings.TrimSpace(os.Getenv("TOOL_RUNTIME_IMAGE")),
		Profile:        "compat",
		ComposeProfile: "compat-mcp",
		Risk:           "medium",
		Approval:       "human",
		Enabled:        definition.Enabled,
		Target: toolcatalog.TargetSpec{
			Input:             "host_file",
			AllowlistRequired: false,
			SupportsMultiple:  true,
		},
		Limits: toolcatalog.LimitSpec{
			TimeoutSeconds: 1800,
			Concurrency:    1,
			MaxTargets:     100,
		},
		Output: toolcatalog.OutputSpec{
			Format:        "text",
			Directory:     "/reports",
			ReportPattern: "*.result.json",
		},
		ResultAdapter: "generic_text",
		Endpoint:      envOr("TOOL_MCP_ENDPOINT", "http://tool-compat-mcp:8080/sse"),
		Description:   definition.Description,
	}
}

func (s *service) registerGenericTool(server *mcp.Server) {
	definition := s.definition
	if definition == nil {
		return
	}
	description := definition.Description
	if strings.TrimSpace(description) == "" {
		description = fmt.Sprintf("Run the configured %s tool inside the selected isolated runtime.", definition.Name)
	}
	server.RegisterTool(mcp.Tool{
		Name:             definition.Name,
		Description:      description,
		ShortDescription: definition.ShortDescription,
		InputSchema:      inputSchemaFromParameters(definition.Parameters),
	}, s.runGeneric)
}

func (s *service) runGeneric(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
	if s.definition == nil {
		return nil, errors.New("generic tool definition is missing")
	}
	if err := validateGenericTargetArguments(args, s.allowedHosts); err != nil {
		return nil, err
	}
	cmdArgs := toolrunner.BuildCommandArgs(s.definition.Name, s.definition, args)
	if len(cmdArgs) == 0 {
		return nil, fmt.Errorf("tool %q is missing a required parameter", s.definition.Name)
	}
	select {
	case s.semaphore <- struct{}{}:
		defer func() { <-s.semaphore }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	jobID := uuid.NewString()
	started := time.Now().UTC()
	timeout := time.Duration(s.tool.Limits.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	stdoutPath := filepath.Join(s.reportsDir, fmt.Sprintf("%s-%s.stdout.txt", s.definition.Name, jobID))
	stderrPath := filepath.Join(s.reportsDir, fmt.Sprintf("%s-%s.stderr.txt", s.definition.Name, jobID))
	stdoutFile, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return nil, fmt.Errorf("create stdout report: %w", err)
	}
	stderrFile, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		_ = stdoutFile.Close()
		return nil, fmt.Errorf("create stderr report: %w", err)
	}
	defer stdoutFile.Close()
	defer stderrFile.Close()
	jobCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(jobCtx, s.definition.Command, cmdArgs...)
	cmd.Dir = envOr("TOOL_WORKDIR", s.reportsDir)
	cmd.Env = append(os.Environ(), "HOME="+envOr("HOME", "/tmp"))
	var stdoutBuffer, stderrBuffer limitedBuffer
	stdoutBuffer.max = 256 * 1024
	stderrBuffer.max = 256 * 1024
	cmd.Stdout = io.MultiWriter(stdoutFile, &stdoutBuffer)
	cmd.Stderr = io.MultiWriter(stderrFile, &stderrBuffer)
	runErr := cmd.Run()
	stdout, stderr := stdoutBuffer.String(), stderrBuffer.String()
	result := runResult{
		JobID:      jobID,
		ToolID:     s.definition.Name,
		Image:      s.tool.Image,
		StartedAt:  started,
		FinishedAt: time.Now().UTC(),
		Status:     "completed",
		TextReport: relativeReportPath(stdoutPath, s.reportsDir),
		ReportFiles: []string{
			filepath.Join(s.reportRefRoot, relativeReportPath(stdoutPath, s.reportsDir)),
			filepath.Join(s.reportRefRoot, relativeReportPath(stderrPath, s.reportsDir)),
		},
		StdoutTail: tail(stdout, 4000),
		StderrTail: tail(stderr, 4000),
	}
	result.ReportSHA256 = make(map[string]string, 2)
	for _, report := range []struct {
		name string
		path string
	}{
		{name: relativeReportPath(stdoutPath, s.reportsDir), path: stdoutPath},
		{name: relativeReportPath(stderrPath, s.reportsDir), path: stderrPath},
	} {
		if digest, hashErr := sha256File(report.path); hashErr == nil {
			result.ReportSHA256[report.name] = digest
		}
	}
	if jobCtx.Err() != nil {
		result.Status = "canceled"
		if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
			result.Status = "timeout"
		}
		result.ExitCode = -1
		result.Error = jobCtx.Err().Error()
	} else if runErr != nil {
		result.Status = "failed"
		result.ExitCode = -1
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		}
		result.Error = runErr.Error()
	}
	if len(result.ReportSHA256) == 0 {
		result.ReportSHA256 = nil
	}
	if writeErr := writeResultMetadata(s.reportsDir, result); writeErr != nil {
		return nil, writeErr
	}
	encoded, _ := json.Marshal(result)
	return &mcp.ToolResult{IsError: result.Status != "completed", Content: []mcp.Content{{Type: "text", Text: string(encoded)}}}, nil
}

type limitedBuffer struct {
	bytes.Buffer
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 {
		return len(p), nil
	}
	remaining := b.max - b.Len()
	if remaining <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.Buffer.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	return b.Buffer.Write(p)
}

func splitIDs(raw string) []string {
	parts := strings.Split(raw, ",")
	ids := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

func selectedToolIDs(selected []selectedTool) []string {
	ids := make([]string, 0, len(selected))
	for _, item := range selected {
		ids = append(ids, item.tool.ID)
	}
	return ids
}

func preflightGenericTools(selected []selectedTool) error {
	missing := make([]string, 0)
	for _, item := range selected {
		if item.definition == nil {
			continue
		}
		command := strings.TrimSpace(item.definition.Command)
		if filepath.IsAbs(command) {
			info, err := os.Stat(command)
			if err != nil || info.IsDir() || info.Mode()&0111 == 0 {
				missing = append(missing, command)
			}
			continue
		}
		if _, err := exec.LookPath(command); err != nil {
			missing = append(missing, command)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("generic tool runtime is missing executable(s): %s; install them in TOOL_RUNTIME_IMAGE or set TOOL_PRECHECK=0 for deferred checks", strings.Join(missing, ", "))
	}
	return nil
}

func isUnsafeGenericCommand(command string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(command)))
	switch base {
	case "sh", "bash", "zsh", "dash", "fish":
		return true
	default:
		return false
	}
}

func envBool(key string, fallback bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func validateGenericTargetArguments(args map[string]interface{}, allowedHosts []string) error {
	if len(allowedHosts) == 0 {
		return nil
	}
	for key, value := range args {
		name := strings.ToLower(key)
		if !containsTargetKey(name) {
			continue
		}
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "http://") || strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "https://") {
			if err := validateTargetAgainst(strings.TrimSpace(text), allowedHosts); err != nil {
				return fmt.Errorf("argument %q: %w", key, err)
			}
		}
	}
	return nil
}

func containsTargetKey(key string) bool {
	for _, part := range []string{"target", "url", "domain", "host", "endpoint", "base_url"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}

func inputSchemaFromParameters(parameters []config.ParameterConfig) map[string]interface{} {
	properties := make(map[string]interface{}, len(parameters))
	required := make([]string, 0)
	for _, parameter := range parameters {
		property := map[string]interface{}{
			"type":        schemaType(parameter.Type),
			"description": parameter.Description,
		}
		if parameter.Default != nil {
			property["default"] = parameter.Default
		}
		if len(parameter.Options) > 0 {
			property["enum"] = parameter.Options
		}
		if parameter.Type == "array" {
			property["items"] = map[string]interface{}{"type": schemaType(parameter.ItemType)}
		}
		properties[parameter.Name] = property
		if parameter.Required {
			required = append(required, parameter.Name)
		}
	}
	schema := map[string]interface{}{"type": "object", "properties": properties}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func schemaType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "int", "integer", "float", "double", "number":
		return "number"
	case "bool", "boolean":
		return "boolean"
	case "array":
		return "array"
	case "object":
		return "object"
	default:
		return "string"
	}
}
