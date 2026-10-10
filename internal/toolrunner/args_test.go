package toolrunner

import (
	"reflect"
	"testing"

	"cyberstrike-ai/internal/config"
)

func TestBuildCommandArgsKeepsNmapScanTypeBeforeFlags(t *testing.T) {
	position := 1
	tool := &config.ToolConfig{
		Name: "nmap",
		Args: []string{"-sT", "-sV", "-sC"},
		Parameters: []config.ParameterConfig{
			{Name: "target", Type: "string", Required: true, Position: &position, Format: "positional"},
			{Name: "ports", Type: "string", Flag: "-p", Format: "flag"},
			{Name: "scan_type", Type: "string", Format: "template", Template: "{value}"},
		},
	}
	got := BuildCommandArgs("nmap", tool, map[string]interface{}{
		"target":    "example.com",
		"ports":     "80, 443",
		"scan_type": "-sT -sV",
	})
	want := []string{"-sT", "-sV", "-p", "80,443", "example.com"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BuildCommandArgs() = %#v, want %#v", got, want)
	}
}

func TestParseAdditionalArgsHonorsQuotes(t *testing.T) {
	got := ParseAdditionalArgs(`--header "X-Test: hello world" --flag`)
	want := []string{"--header", "X-Test: hello world", "--flag"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseAdditionalArgs() = %#v, want %#v", got, want)
	}
}

func TestParseAdditionalArgsUnescapesQuote(t *testing.T) {
	got := ParseAdditionalArgs(`--header \"X-Test\"`)
	want := []string{"--header", `"X-Test"`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseAdditionalArgs() = %#v, want %#v", got, want)
	}
}
