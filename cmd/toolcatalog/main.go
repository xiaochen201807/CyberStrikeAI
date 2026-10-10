// toolcatalog validates the external tool-plane catalog used by Compose.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"cyberstrike-ai/internal/toolcatalog"
)

func main() {
	if len(os.Args) < 3 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "validate":
		validate(os.Args[2:])
	case "list":
		list(os.Args[2:])
	case "inventory":
		inventory(os.Args[2:])
	case "doctor":
		doctor(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func inventory(args []string) {
	fs := flag.NewFlagSet("inventory", flag.ExitOnError)
	toolsDir := fs.String("tools-dir", "tools", "repository tool definition directory")
	output := fs.String("output", "", "write YAML to this path; stdout when empty")
	catalogPath := fs.String("catalog", "deploy/tools/catalog.yaml", "deployment catalog used to mark adapter-ready tools")
	runtimePath := fs.String("runtime", "deploy/tools/runtime/families.yaml", "family runtime manifest; empty disables family mapping")
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "inventory does not accept positional arguments")
		os.Exit(2)
	}

	catalog, err := toolcatalog.Load(*catalogPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "catalog invalid: %v\n", err)
		os.Exit(1)
	}
	inv, err := toolcatalog.BuildInventoryWithRuntime(*toolsDir, catalog, *runtimePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "inventory failed: %v\n", err)
		os.Exit(1)
	}
	data, err := toolcatalog.MarshalInventory(inv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal inventory: %v\n", err)
		os.Exit(1)
	}
	if *output == "" {
		_, _ = os.Stdout.Write(data)
		return
	}
	if err := os.MkdirAll(filepath.Dir(*output), 0755); err != nil {
		fmt.Fprintf(os.Stderr, "create output directory: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*output, data, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write inventory: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("inventory written: %s (%d tools, %d enabled, %d disabled)\n", *output, inv.Total, inv.Enabled, inv.Disabled)
}

func doctor(args []string) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	toolsDir := fs.String("tools-dir", "tools", "repository tool definition directory")
	includeDisabled := fs.Bool("include-disabled", false, "also check disabled definitions")
	family := fs.String("family", "", "only check one classified runtime family")
	strict := fs.Bool("strict", false, "exit non-zero when an enabled executable is missing")
	_ = fs.Parse(args)
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "doctor does not accept positional arguments")
		os.Exit(2)
	}
	checks, err := toolcatalog.CheckRuntimeFamily(*toolsDir, *includeDisabled, *family)
	if err != nil {
		fmt.Fprintf(os.Stderr, "runtime check failed: %v\n", err)
		os.Exit(1)
	}
	missing := 0
	for _, check := range checks {
		path := check.Path
		if path == "" {
			path = "-"
		}
		fmt.Printf("%-28s %-10s %-24s %s\n", check.ID, check.Status, check.Command, path)
		if check.Status == "missing" {
			missing++
		}
	}
	if *strict && missing > 0 {
		fmt.Fprintf(os.Stderr, "runtime check failed: %d enabled commands are missing\n", missing)
		os.Exit(1)
	}
}

func validate(args []string) {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	requireDigests := fs.Bool("require-digests", false, "require sha256 image digests")
	composePath := fs.String("compose", "", "optional Compose file to cross-check service names and profiles")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "validate requires exactly one catalog path")
		os.Exit(2)
	}
	catalog, err := toolcatalog.Load(fs.Arg(0))
	if err == nil {
		err = catalog.Validate(toolcatalog.ValidateOptions{RequireDigests: *requireDigests})
	}
	if err == nil && *composePath != "" {
		err = catalog.ValidateCompose(*composePath)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "catalog invalid: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("adapter catalog valid: %d adapters, %d profiles\n", len(catalog.Tools), len(catalog.Profiles))
}

func list(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "list requires exactly one catalog path")
		os.Exit(2)
	}
	catalog, err := toolcatalog.Load(fs.Arg(0))
	if err == nil {
		err = catalog.Validate(toolcatalog.ValidateOptions{})
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "catalog invalid: %v\n", err)
		os.Exit(1)
	}
	ids := make([]string, 0, len(catalog.Tools))
	for _, tool := range catalog.Tools {
		ids = append(ids, tool.ID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		tool, _ := catalog.Tool(id)
		composeProfile := tool.ComposeProfile
		if composeProfile == "" {
			composeProfile = tool.Profile
		}
		fmt.Printf("%-20s profile=%-8s compose=%-12s risk=%-8s kind=%s service=%s\n", tool.ID, tool.Profile, composeProfile, tool.Risk, tool.Kind, tool.Service)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: toolcatalog validate [--require-digests] [--compose compose.yaml] <catalog.yaml>")
	fmt.Fprintln(os.Stderr, "       toolcatalog list <catalog.yaml>")
	fmt.Fprintln(os.Stderr, "       toolcatalog inventory [--tools-dir tools] [--catalog deploy/tools/catalog.yaml] [--runtime deploy/tools/runtime/families.yaml] [--output deploy/tools/inventory.yaml]")
	fmt.Fprintln(os.Stderr, "       toolcatalog doctor [--tools-dir tools] [--family family] [--include-disabled] [--strict]")
}
