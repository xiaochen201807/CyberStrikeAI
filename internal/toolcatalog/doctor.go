package toolcatalog

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"cyberstrike-ai/internal/config"
)

type ToolCheck struct {
	ID      string
	Command string
	Status  string // available | missing | disabled | builtin
	Path    string
}

// CheckRuntime verifies the executable side of tools/*.yaml in the current
// runtime. It intentionally does not mutate the host or install packages.
func CheckRuntime(toolsDir string, includeDisabled bool) ([]ToolCheck, error) {
	return CheckRuntimeFamily(toolsDir, includeDisabled, "")
}

// CheckRuntimeFamily is the family-scoped variant used by the independent
// runtime images.  Keeping the filter here means the image build and the
// repository inventory use the same family classification instead of carrying
// a second hand-maintained command list in shell.
func CheckRuntimeFamily(toolsDir string, includeDisabled bool, family string) ([]ToolCheck, error) {
	tools, err := config.LoadToolsFromDir(toolsDir)
	if err != nil {
		return nil, fmt.Errorf("load tool definitions: %w", err)
	}
	family = strings.ToLower(strings.TrimSpace(family))
	checks := make([]ToolCheck, 0, len(tools))
	for _, tool := range tools {
		if family != "" && ClassifyToolFamily(tool.Name, tool.Command) != family {
			continue
		}
		check := ToolCheck{ID: tool.Name, Command: tool.Command}
		if strings.HasPrefix(strings.TrimSpace(tool.Command), "internal:") {
			check.Status = "builtin"
			checks = append(checks, check)
			continue
		}
		if !tool.Enabled {
			check.Status = "disabled"
			if !includeDisabled {
				continue
			}
			checks = append(checks, check)
			continue
		}
		if filepath.IsAbs(tool.Command) {
			if info, statErr := os.Stat(tool.Command); statErr == nil && !info.IsDir() && info.Mode()&0111 != 0 {
				check.Status = "available"
				check.Path = tool.Command
			} else {
				check.Status = "missing"
			}
		} else if path, lookErr := exec.LookPath(tool.Command); lookErr == nil {
			check.Status = "available"
			check.Path = path
		} else {
			check.Status = "missing"
		}
		checks = append(checks, check)
	}
	return checks, nil
}
