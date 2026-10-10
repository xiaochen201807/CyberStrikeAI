package toolcatalog

import (
	"os"
	"path/filepath"
	"testing"
)

func validCatalog() Catalog {
	return Catalog{
		SchemaVersion: 1,
		Profiles:      map[string]Profile{"web": {}},
		Tools: []Tool{{
			ID: "nuclei", Service: "nuclei-scan", Kind: "job", Transport: "compose",
			Image: "projectdiscovery/nuclei:3.4.10", Profile: "web", Risk: "medium", Approval: "human", Enabled: true,
			Target: TargetSpec{Input: "url_file", AllowlistRequired: true, SupportsMultiple: true},
			Limits: LimitSpec{TimeoutSeconds: 600, Rate: 30, Concurrency: 5, MaxTargets: 100},
			Output: OutputSpec{Format: "jsonl", Directory: "/reports", ReportPattern: "nuclei-*.jsonl"},
		}},
	}
}

func TestValidateAcceptsVersionPinnedTool(t *testing.T) {
	if err := validCatalog().Validate(ValidateOptions{}); err != nil {
		t.Fatalf("expected valid catalog, got %v", err)
	}
}

func TestValidateRejectsFloatingLatestImage(t *testing.T) {
	catalog := validCatalog()
	catalog.Tools[0].Image = "projectdiscovery/nuclei:latest"
	if err := catalog.Validate(ValidateOptions{}); err == nil {
		t.Fatal("expected latest image to be rejected")
	}
}

func TestValidateRequiresDigestWhenRequested(t *testing.T) {
	if err := validCatalog().Validate(ValidateOptions{RequireDigests: true}); err == nil {
		t.Fatal("expected digest requirement to fail for a tag-only image")
	}
}

func TestValidateRejectsHighRiskWithoutHumanApproval(t *testing.T) {
	catalog := validCatalog()
	catalog.Tools[0].Risk = "high"
	catalog.Tools[0].Approval = "none"
	if err := catalog.Validate(ValidateOptions{}); err == nil {
		t.Fatal("expected high-risk tool without approval to be rejected")
	}
}

func TestValidateComposeChecksCatalogServicesAndProfiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "compose.yaml")
	compose := []byte("services:\n  other-service:\n    profiles: [web]\n")
	if err := os.WriteFile(path, compose, 0600); err != nil {
		t.Fatal(err)
	}
	catalog := validCatalog()
	if err := catalog.ValidateCompose(path); err == nil {
		t.Fatal("expected service mismatch to fail")
	}
	catalog.Tools[0].Service = "other-service"
	if err := catalog.ValidateCompose(path); err != nil {
		t.Fatalf("expected compose contract to pass: %v", err)
	}
}
