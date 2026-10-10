// Package toolrunner contains the command-line compatibility layer shared by
// the in-process executor and the isolated tool-plane adapters.  Keeping the
// argument mapping in one package is what lets a tools/*.yaml definition keep
// the same API when its binary moves to a separate container.
package toolrunner

import (
	"encoding/json"
	"fmt"
	"strings"

	"cyberstrike-ai/internal/config"
)

// BuildCommandArgs applies a ToolConfig's fixed arguments and parameter
// mapping rules.  It intentionally returns an empty slice when a required
// parameter is missing, matching the existing Executor contract.
func BuildCommandArgs(toolName string, toolConfig *config.ToolConfig, args map[string]interface{}) []string {
	if toolConfig == nil {
		return nil
	}
	cmdArgs := make([]string, 0)
	if len(toolConfig.Parameters) == 0 {
		cmdArgs = append(cmdArgs, toolConfig.Args...)
		for key, value := range args {
			if key == "_tool_name" {
				continue
			}
			cmdArgs = append(cmdArgs, fmt.Sprintf("--%s", key))
			if strValue, ok := value.(string); ok {
				cmdArgs = append(cmdArgs, strValue)
			} else {
				cmdArgs = append(cmdArgs, fmt.Sprintf("%v", value))
			}
		}
		return cmdArgs
	}

	hasScanType := false
	scanTypeValue := ""
	if scanType, ok := args["scan_type"].(string); ok && scanType != "" {
		hasScanType = true
		scanTypeValue = scanType
	}
	if !(hasScanType && toolName == "nmap") {
		cmdArgs = append(cmdArgs, toolConfig.Args...)
	}

	positional := make([]config.ParameterConfig, 0)
	flags := make([]config.ParameterConfig, 0)
	for _, param := range toolConfig.Parameters {
		if param.Position != nil {
			positional = append(positional, param)
		} else {
			flags = append(flags, param)
		}
	}

	// A position-zero subcommand (for example gobuster dir) must precede flags.
	for _, param := range positional {
		if isSpecialParam(param.Name) || param.Position == nil || *param.Position != 0 {
			continue
		}
		value := paramValue(args, param)
		if value == nil && param.Default != nil {
			value = param.Default
		}
		if value != nil {
			cmdArgs = append(cmdArgs, formatParamValue(param, value))
		}
		break
	}

	for _, param := range flags {
		if isSpecialParam(param.Name) {
			continue
		}
		value := paramValue(args, param)
		if value == nil {
			if param.Required {
				return nil
			}
			continue
		}
		if param.Type == "bool" {
			boolValue, ok := boolParam(value)
			if ok {
				if !boolValue {
					continue
				}
				if param.Flag != "" {
					cmdArgs = append(cmdArgs, param.Flag)
				}
				continue
			}
		}
		formatted := formatParamValue(param, value)
		if strings.TrimSpace(formatted) == "" {
			if param.Required {
				return nil
			}
			continue
		}
		format := param.Format
		if format == "" {
			format = "flag"
		}
		switch format {
		case "flag":
			if param.Flag != "" {
				cmdArgs = append(cmdArgs, param.Flag)
			}
			cmdArgs = append(cmdArgs, formatted)
		case "combined":
			if param.Flag != "" {
				cmdArgs = append(cmdArgs, fmt.Sprintf("%s=%s", param.Flag, formatted))
			} else {
				cmdArgs = append(cmdArgs, formatted)
			}
		case "template":
			template := strings.ReplaceAll(param.Template, "{flag}", param.Flag)
			template = strings.ReplaceAll(template, "{value}", formatted)
			template = strings.ReplaceAll(template, "{name}", param.Name)
			if template == "" {
				if param.Flag != "" {
					cmdArgs = append(cmdArgs, param.Flag)
				}
				cmdArgs = append(cmdArgs, formatted)
			} else {
				cmdArgs = append(cmdArgs, strings.Fields(template)...)
			}
		case "positional":
			cmdArgs = append(cmdArgs, formatted)
		default:
			cmdArgs = append(cmdArgs, formatted)
		}
	}

	maxPosition := -1
	for _, param := range positional {
		if param.Position != nil && *param.Position > maxPosition {
			maxPosition = *param.Position
		}
	}
	for position := 0; position <= maxPosition; position++ {
		if position == 0 {
			continue
		}
		for _, param := range positional {
			if isSpecialParam(param.Name) || param.Position == nil || *param.Position != position {
				continue
			}
			value := paramValue(args, param)
			if value == nil {
				if param.Required {
					return nil
				}
				if param.Default != nil {
					value = param.Default
				} else {
					break
				}
			}
			cmdArgs = append(cmdArgs, formatParamValue(param, value))
			break
		}
	}

	if additional, ok := args["additional_args"].(string); ok && additional != "" {
		cmdArgs = append(cmdArgs, ParseAdditionalArgs(additional)...)
	}
	if hasScanType {
		scanArgs := ParseAdditionalArgs(scanTypeValue)
		if len(scanArgs) > 0 {
			if toolName == "nmap" {
				return append(scanArgs, cmdArgs...)
			}
			insertPos := len(cmdArgs)
			for i := len(cmdArgs) - 1; i >= 0; i-- {
				if !strings.HasPrefix(cmdArgs[i], "-") {
					insertPos = i
					break
				}
			}
			out := make([]string, 0, len(cmdArgs)+len(scanArgs))
			out = append(out, cmdArgs[:insertPos]...)
			out = append(out, scanArgs...)
			out = append(out, cmdArgs[insertPos:]...)
			cmdArgs = out
		}
	}
	return cmdArgs
}

// ParseAdditionalArgs splits a shell-like argument string without invoking a
// shell.  It keeps quoted values together and is shared by both runtimes.
func ParseAdditionalArgs(value string) []string {
	if value == "" {
		return []string{}
	}
	result := make([]string, 0)
	var current strings.Builder
	inQuotes := false
	var quote rune
	escape := false
	runes := []rune(value)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if escape {
			current.WriteRune(r)
			escape = false
			continue
		}
		if r == '\\' {
			if i+1 < len(runes) && (runes[i+1] == '\'' || runes[i+1] == '"') {
				i++
				current.WriteRune(runes[i])
				continue
			}
			escape = true
			current.WriteRune(r)
			continue
		}
		if !inQuotes && (r == '\'' || r == '"') {
			inQuotes = true
			quote = r
			continue
		}
		if inQuotes && r == quote {
			inQuotes = false
			quote = 0
			continue
		}
		if !inQuotes && (r == ' ' || r == '\t' || r == '\n') {
			if current.Len() > 0 {
				result = append(result, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteRune(r)
	}
	if current.Len() > 0 {
		result = append(result, current.String())
	}
	if len(result) == 0 {
		return strings.Fields(value)
	}
	return result
}

func isSpecialParam(name string) bool {
	return name == "additional_args" || name == "scan_type" || name == "action"
}

func paramValue(args map[string]interface{}, param config.ParameterConfig) interface{} {
	if value, ok := args[param.Name]; ok && value != nil {
		return value
	}
	if param.Required {
		return nil
	}
	return param.Default
}

func boolParam(value interface{}) (bool, bool) {
	switch value := value.(type) {
	case bool:
		return value, true
	case float64:
		return value != 0, true
	case int:
		return value != 0, true
	case string:
		return value == "true" || value == "1" || value == "yes", true
	default:
		return false, false
	}
}

func formatParamValue(param config.ParameterConfig, value interface{}) string {
	switch param.Type {
	case "bool":
		if boolValue, ok := value.(bool); ok {
			return fmt.Sprintf("%v", boolValue)
		}
		return "false"
	case "array":
		if array, ok := value.([]interface{}); ok {
			items := make([]string, 0, len(array))
			for _, item := range array {
				items = append(items, fmt.Sprintf("%v", item))
			}
			return strings.Join(items, ",")
		}
		return fmt.Sprintf("%v", value)
	case "object":
		if data, err := json.Marshal(value); err == nil {
			return string(data)
		}
		return fmt.Sprintf("%v", value)
	default:
		formatted := fmt.Sprintf("%v", value)
		if param.Name == "ports" {
			formatted = strings.ReplaceAll(formatted, " ", "")
		}
		return formatted
	}
}
