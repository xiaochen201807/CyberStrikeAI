// tool-mcp exposes catalog-approved scanners, or an opt-in family subset of
// the repository's tools/*.yaml definitions, as an authenticated SSE MCP
// service. The bridge is copied into the selected runtime image, so the
// control plane never needs the scanner binary or Docker Socket.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"cyberstrike-ai/internal/config"
	"cyberstrike-ai/internal/mcp"
	"cyberstrike-ai/internal/toolcatalog"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	defaultCatalogPath = "/etc/cyberstrike/catalog.yaml"
	defaultListen      = ":8080"
	maxTargetRunes     = 2048
)

var hostPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)

type service struct {
	tool          toolcatalog.Tool
	definition    *config.ToolConfig
	generic       bool
	reportsDir    string
	semaphore     chan struct{}
	logger        *zap.Logger
	allowedHosts  []string
	reportRefRoot string
}

type runResult struct {
	JobID          string            `json:"job_id"`
	ToolID         string            `json:"tool_id"`
	Image          string            `json:"image,omitempty"`
	Target         string            `json:"target"`
	StartedAt      time.Time         `json:"started_at"`
	FinishedAt     time.Time         `json:"finished_at"`
	ExitCode       int               `json:"exit_code"`
	Status         string            `json:"status"`
	TextReport     string            `json:"text_report,omitempty"`
	JSONReport     string            `json:"json_report,omitempty"`
	ReportFiles    []string          `json:"report_files,omitempty"`
	ReportSHA256   map[string]string `json:"report_sha256,omitempty"`
	JSONPreview    string            `json:"json_preview,omitempty"`
	PreviewTrimmed bool              `json:"preview_trimmed,omitempty"`
	StdoutTail     string            `json:"stdout_tail,omitempty"`
	StderrTail     string            `json:"stderr_tail,omitempty"`
	Error          string            `json:"error,omitempty"`
}

func main() {
	var (
		catalogPath = flag.String("catalog", envOr("TOOL_CATALOG", defaultCatalogPath), "catalog path")
		listen      = flag.String("listen", envOr("TOOL_MCP_LISTEN", defaultListen), "HTTP listen address")
		healthcheck = flag.Bool("healthcheck", false, "validate the image configuration and exit")
		probe       = flag.Bool("probe", false, "probe the running HTTP health endpoint and exit")
	)
	flag.Parse()

	logger, err := zap.NewProduction()
	if err != nil {
		fmt.Fprintf(os.Stderr, "logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	catalog, err := toolcatalog.Load(*catalogPath)
	if err != nil {
		fatal(logger, err)
	}
	if err := catalog.Validate(toolcatalog.ValidateOptions{}); err != nil {
		fatal(logger, fmt.Errorf("catalog validation: %w", err))
	}
	selected, genericMode, err := selectTools(catalog)
	if err != nil {
		fatal(logger, err)
	}
	if genericMode && envBool("TOOL_PRECHECK", true) {
		if err := preflightGenericTools(selected); err != nil {
			fatal(logger, err)
		}
	}
	token := strings.TrimSpace(os.Getenv("TOOL_MCP_TOKEN"))
	if err := validateServiceToken(token); err != nil {
		fatal(logger, err)
	}
	var allowedHosts []string
	if genericMode {
		allowedHosts, err = parseOptionalAllowedHosts(os.Getenv("TOOL_ALLOWED_HOSTS"))
	} else {
		allowedHosts, err = parseAllowedHosts(os.Getenv("TOOL_ALLOWED_HOSTS"))
	}
	if err != nil {
		fatal(logger, err)
	}
	if *healthcheck {
		return
	}
	if *probe {
		probeURL := envOr("TOOL_MCP_PROBE_URL", "http://127.0.0.1:8080/healthz")
		response, probeErr := http.Get(probeURL)
		if probeErr != nil {
			fatal(logger, fmt.Errorf("health probe: %w", probeErr))
		}
		_ = response.Body.Close()
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			fatal(logger, fmt.Errorf("health probe returned HTTP %d", response.StatusCode))
		}
		return
	}

	reportsDir := strings.TrimSpace(envOr("REPORTS_DIR", "/reports"))
	if err := os.MkdirAll(reportsDir, 0750); err != nil {
		fatal(logger, fmt.Errorf("create reports directory: %w", err))
	}
	services := make([]*service, 0, len(selected))
	for _, item := range selected {
		concurrency := item.tool.Limits.Concurrency
		if concurrency <= 0 {
			concurrency = 1
		}
		services = append(services, &service{
			tool:          item.tool,
			definition:    item.definition,
			generic:       item.generic,
			reportsDir:    reportsDir,
			semaphore:     make(chan struct{}, concurrency),
			logger:        logger,
			allowedHosts:  allowedHosts,
			reportRefRoot: strings.TrimSpace(envOr("REPORT_REFERENCE_ROOT", reportsDir)),
		})
	}

	mcpServer := mcp.NewServer(logger)
	allCatalogTools := append([]toolcatalog.Tool(nil), catalog.Tools...)
	for _, item := range selected {
		if item.generic {
			allCatalogTools = append(allCatalogTools, item.tool)
		}
	}
	registerCatalogTool(mcpServer, allCatalogTools)
	for _, svc := range services {
		svc.registerScannerTool(mcpServer)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	protected := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := strings.TrimSpace(os.Getenv("TOOL_MCP_TOKEN"))
			provided := r.Header.Get(envOr("TOOL_MCP_AUTH_HEADER", "X-Tool-Token"))
			if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	// The existing MCP server implements the 2024-11-05 SSE handshake used by
	// CyberStrikeAI's external MCP manager. /mcp also accepts a simple POST for
	// smoke tests and non-SSE clients.
	mux.Handle("/sse", protected(http.HandlerFunc(mcpServer.HandleHTTP)))
	mux.Handle("/mcp", protected(http.HandlerFunc(mcpServer.HandleHTTP)))

	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	logger.Info("tool MCP service listening", zap.String("tools", strings.Join(selectedToolIDs(selected), ",")), zap.String("address", *listen))
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fatal(logger, err)
	}
}

func (s *service) registerScannerTool(server *mcp.Server) {
	if s.generic {
		s.registerGenericTool(server)
		return
	}
	name := s.tool.ID
	description := s.tool.Description
	if description == "" {
		description = fmt.Sprintf("Run the catalog-approved %s scanner against one authorized target.", name)
	}
	server.RegisterTool(mcp.Tool{
		Name:             name,
		Description:      description,
		ShortDescription: "Catalog-constrained scanner job",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"target_url": map[string]interface{}{
					"type":        "string",
					"description": "One exact http(s) URL already approved in the CyberStrikeAI project.",
				},
				"severity": map[string]interface{}{
					"type":        "string",
					"description": "Optional comma-separated severity filter for Nuclei.",
				},
				"rate_limit": map[string]interface{}{
					"type":        "integer",
					"description": "Optional requests per second; capped by the catalog.",
				},
				"concurrency": map[string]interface{}{
					"type":        "integer",
					"description": "Optional scanner concurrency; capped by the catalog.",
				},
				"spider_minutes": map[string]interface{}{
					"type":        "integer",
					"description": "Optional ZAP baseline spider duration.",
				},
			},
			"required": []string{"target_url"},
		},
	}, s.run)
}

func validateToolAdapter(tool toolcatalog.Tool) error {
	switch tool.ID {
	case "nuclei_scan", "zap_baseline":
		return nil
	default:
		return fmt.Errorf("tool %q has no implemented MCP command adapter", tool.ID)
	}
}

func (s *service) run(ctx context.Context, args map[string]interface{}) (*mcp.ToolResult, error) {
	target, err := stringArg(args, "target_url")
	if err != nil {
		return nil, err
	}
	if err := validateTargetAgainst(target, s.allowedHosts); err != nil {
		return nil, err
	}

	select {
	case s.semaphore <- struct{}{}:
		defer func() { <-s.semaphore }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	jobID := uuid.NewString()
	started := time.Now().UTC()
	textReport := filepath.Join(s.reportsDir, fmt.Sprintf("%s-%s.txt", s.tool.ID, jobID))
	jsonReport := filepath.Join(s.reportsDir, fmt.Sprintf("%s-%s.json", s.tool.ID, jobID))
	htmlReport := filepath.Join(s.reportsDir, fmt.Sprintf("%s-%s.html", s.tool.ID, jobID))

	var command []string
	switch s.tool.ID {
	case "nuclei_scan":
		severity := stringOr(args, "severity", "medium,high,critical")
		if err := validateSeverity(severity); err != nil {
			return nil, err
		}
		rate := boundedInt(args, "rate_limit", s.tool.Limits.Rate, 1, 1000)
		concurrency := boundedInt(args, "concurrency", s.tool.Limits.Concurrency, 1, 100)
		command = []string{
			"nuclei", "-u", target,
			"-severity", severity,
			"-rate-limit", strconv.Itoa(rate),
			"-concurrency", strconv.Itoa(concurrency),
			"-bulk-size", "1",
			"-o", textReport,
			"-json-export", jsonReport,
			"-no-color",
			"-no-interactsh",
		}
	case "zap_baseline":
		minutes := boundedInt(args, "spider_minutes", 1, 1, 30)
		command = []string{
			"zap-baseline.py", "-t", target,
			"-m", strconv.Itoa(minutes),
			"-z", "-config view.locale=" + envOr("ZAP_LOCALE", "zh_CN"),
			"-r", filepath.Base(htmlReport),
			"-J", filepath.Base(jsonReport),
			"-I",
		}
	default:
		return nil, fmt.Errorf("tool %q has no safe command adapter", s.tool.ID)
	}

	timeout := time.Duration(s.tool.Limits.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	jobCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(jobCtx, command[0], command[1:]...)
	cmd.Dir = s.reportsDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()

	result := runResult{
		JobID:       jobID,
		ToolID:      s.tool.ID,
		Image:       s.tool.Image,
		Target:      target,
		StartedAt:   started,
		FinishedAt:  time.Now().UTC(),
		Status:      "completed",
		TextReport:  relativeReportPath(textReport, s.reportsDir),
		JSONReport:  relativeReportPath(jsonReport, s.reportsDir),
		ReportFiles: []string{},
		StdoutTail:  tail(stdout.String(), 4000),
		StderrTail:  tail(stderr.String(), 4000),
	}
	result.ReportFiles = []string{
		filepath.Join(s.reportRefRoot, result.TextReport),
		filepath.Join(s.reportRefRoot, result.JSONReport),
	}
	if jobCtx.Err() != nil {
		result.Status = "canceled"
		if errors.Is(jobCtx.Err(), context.DeadlineExceeded) {
			result.Status = "timeout"
		}
		result.ExitCode = -1
		result.Error = jobCtx.Err().Error()
	} else if err != nil {
		result.Status = "failed"
		result.ExitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.ExitCode = exitErr.ExitCode()
		}
		result.Error = err.Error()
	}
	if s.tool.ID == "zap_baseline" {
		// ZAP writes the reports relative to cmd.Dir. Keep the returned paths
		// consistent with Nuclei's absolute output paths.
		result.TextReport = relativeReportPath(htmlReport, s.reportsDir)
		result.JSONReport = relativeReportPath(filepath.Join(s.reportsDir, filepath.Base(jsonReport)), s.reportsDir)
		result.ReportFiles = []string{
			filepath.Join(s.reportRefRoot, result.TextReport),
			filepath.Join(s.reportRefRoot, result.JSONReport),
		}
	}
	result.ReportSHA256 = make(map[string]string)
	for _, report := range []struct {
		name string
		path string
	}{
		{name: result.TextReport, path: filepath.Join(s.reportsDir, result.TextReport)},
		{name: result.JSONReport, path: filepath.Join(s.reportsDir, result.JSONReport)},
	} {
		if digest, hashErr := sha256File(report.path); hashErr == nil {
			result.ReportSHA256[report.name] = digest
		}
		// The control-plane container uses a different non-root UID while
		// reading the shared directory through a read-only mount. Keep only
		// these adapter-created reports readable across that boundary.
		_ = os.Chmod(report.path, 0644)
	}
	if len(result.ReportSHA256) == 0 {
		result.ReportSHA256 = nil
	}
	if preview, trimmed, previewErr := readReportPreview(filepath.Join(s.reportsDir, filepath.Base(jsonReport)), 64*1024); previewErr == nil {
		result.JSONPreview = preview
		result.PreviewTrimmed = trimmed
	}
	if writeErr := writeResultMetadata(s.reportsDir, result); writeErr != nil {
		return nil, writeErr
	}

	encoded, _ := json.Marshal(result)
	return &mcp.ToolResult{
		IsError: result.Status != "completed",
		Content: []mcp.Content{{Type: "text", Text: string(encoded)}},
	}, nil
}

func registerCatalogTool(server *mcp.Server, tools []toolcatalog.Tool) {
	server.RegisterTool(mcp.Tool{
		Name:             "tool_catalog",
		Description:      "List the enabled tools, profiles, risk and target contracts in the mounted catalog.",
		ShortDescription: "Read-only tool catalog",
		InputSchema:      map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
	}, func(context.Context, map[string]interface{}) (*mcp.ToolResult, error) {
		entries := make([]map[string]interface{}, 0, len(tools))
		for _, tool := range tools {
			if !tool.Enabled {
				continue
			}
			entries = append(entries, map[string]interface{}{
				"id": tool.ID, "service": tool.Service, "kind": tool.Kind,
				"transport": tool.Transport, "profile": tool.Profile,
				"compose_profile": tool.ComposeProfile, "risk": tool.Risk,
				"approval": tool.Approval, "target": tool.Target, "limits": tool.Limits,
				"output": tool.Output, "result_adapter": tool.ResultAdapter,
				"endpoint": tool.Endpoint,
			})
		}
		data, _ := json.Marshal(map[string]interface{}{"tools": entries})
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: string(data)}}}, nil
	})
}

func stringArg(args map[string]interface{}, key string) (string, error) {
	value, ok := args[key]
	if !ok {
		return "", fmt.Errorf("missing required argument %q", key)
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("argument %q must be a non-empty string", key)
	}
	return strings.TrimSpace(text), nil
}

func stringOr(args map[string]interface{}, key, fallback string) string {
	value, ok := args[key].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func boundedInt(args map[string]interface{}, key string, fallback, min, max int) int {
	value := fallback
	switch raw := args[key].(type) {
	case float64:
		value = int(raw)
	case int:
		value = raw
	case json.Number:
		if n, err := raw.Int64(); err == nil {
			value = int(n)
		}
	}
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func validateTarget(target string) error {
	if len([]rune(target)) > maxTargetRunes {
		return fmt.Errorf("target_url exceeds %d characters", maxTargetRunes)
	}
	parsed, err := url.ParseRequestURI(target)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("target_url must use http:// or https://")
	}
	if parsed.User != nil {
		return errors.New("target_url must not contain embedded credentials")
	}
	if strings.ContainsAny(target, "\r\n\x00") {
		return errors.New("target_url contains a prohibited control character")
	}
	return nil
}

func validateTargetAgainst(target string, allowedHosts []string) error {
	if err := validateTarget(target); err != nil {
		return err
	}
	parsed, _ := url.ParseRequestURI(target)
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	for _, pattern := range allowedHosts {
		if pattern == host {
			return nil
		}
		if strings.HasPrefix(pattern, "*.") && strings.HasSuffix(host, pattern[1:]) && host != pattern[2:] {
			return nil
		}
	}
	return fmt.Errorf("target host %q is outside TOOL_ALLOWED_HOSTS", host)
}

func parseAllowedHosts(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	allowed := make([]string, 0, len(parts))
	for _, part := range parts {
		host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(part), "."))
		if host == "" {
			continue
		}
		if strings.Contains(host, "://") || strings.ContainsAny(host, "/\r\n\x00") {
			return nil, fmt.Errorf("TOOL_ALLOWED_HOSTS contains an invalid host pattern %q", part)
		}
		if host == "*" || host == "*." {
			return nil, errors.New("TOOL_ALLOWED_HOSTS cannot contain a global wildcard")
		}
		if strings.HasPrefix(host, "*.") {
			if strings.ContainsAny(host[2:], "*?") || !hostPattern.MatchString(host[2:]) {
				return nil, fmt.Errorf("TOOL_ALLOWED_HOSTS contains an invalid host pattern %q", part)
			}
		} else if strings.ContainsAny(host, "*?") || !hostPattern.MatchString(host) {
			return nil, fmt.Errorf("TOOL_ALLOWED_HOSTS contains an invalid host pattern %q", part)
		}
		allowed = append(allowed, host)
	}
	if len(allowed) == 0 {
		return nil, errors.New("TOOL_ALLOWED_HOSTS must contain at least one exact host or *.example.com pattern")
	}
	return allowed, nil
}

func parseOptionalAllowedHosts(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	return parseAllowedHosts(raw)
}

func validateSeverity(value string) error {
	for _, item := range strings.Split(value, ",") {
		severity := strings.TrimSpace(strings.ToLower(item))
		switch severity {
		case "info", "low", "medium", "high", "critical", "unknown":
		default:
			return fmt.Errorf("unsupported Nuclei severity %q", item)
		}
	}
	return nil
}

func validateServiceToken(token string) error {
	if token == "" {
		return errors.New("TOOL_MCP_TOKEN must be set; refuse to start an unauthenticated tool service")
	}
	if len(token) < 32 || strings.Contains(strings.ToLower(token), "replace-with") {
		return errors.New("TOOL_MCP_TOKEN must be a non-placeholder value of at least 32 characters")
	}
	return nil
}

func relativeReportPath(path, root string) string {
	if rel, err := filepath.Rel(root, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.Base(path)
}

func writeResultMetadata(root string, result runResult) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(root, fmt.Sprintf("%s-%s.result.json", result.ToolID, result.JobID))
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func tail(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[len(runes)-max:])
}

func readReportPreview(path string, maxBytes int) (string, bool, error) {
	if maxBytes <= 0 {
		return "", false, errors.New("preview size must be positive")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return "", false, err
	}
	if len(data) <= maxBytes {
		return string(data), false, nil
	}
	return string(data[:maxBytes]), true, nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func fatal(logger *zap.Logger, err error) {
	logger.Error("tool MCP service failed", zap.Error(err))
	os.Exit(1)
}
