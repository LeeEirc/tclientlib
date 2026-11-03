package cli

import (
	"bytes"
	"io"
	"runtime/debug"
	"strings"
	"testing"
)

func TestVersionDoesNotRequireHostOrInput(t *testing.T) {
	var output, diagnostics bytes.Buffer
	if code := Run([]string{"--version"}, nil, &output, &diagnostics); code != 0 {
		t.Fatalf("exit %d: %s", code, diagnostics.String())
	}
	if got := output.String(); !strings.HasPrefix(got, "tclient ") || strings.Count(got, "\n") != 1 {
		t.Fatalf("unexpected version output: %q", got)
	}
	if diagnostics.Len() != 0 {
		t.Fatalf("version wrote to stderr: %q", diagnostics.String())
	}
}

func TestVersionStillRejectsInvalidFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--version", "--unknown"},
		{"--version=maybe"},
		{"--version=false"},
	} {
		var output, diagnostics bytes.Buffer
		if code := Run(args, nil, &output, &diagnostics); code != 2 {
			t.Errorf("%q: exit %d, want 2; %s", args, code, diagnostics.String())
		}
		if output.Len() != 0 {
			t.Errorf("%q: wrote version despite invalid flags: %q", args, output.String())
		}
	}
}

func TestHelpListsVersionWithoutInput(t *testing.T) {
	var output bytes.Buffer
	if code := Run([]string{"--help"}, nil, io.Discard, &output); code != 0 {
		t.Fatalf("help exit %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "-version") {
		t.Fatalf("version missing from help: %s", output.String())
	}
}

func TestVersionMetadataPrecedence(t *testing.T) {
	info := &debug.BuildInfo{
		Main: debug.Module{Version: "v0.1.0"},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: "source-commit"},
			{Key: "vcs.modified", Value: "true"},
			{Key: "vcs.time", Value: "2026-01-01T00:00:00Z"},
		},
	}
	if got := formatVersion("dev", "unknown", "unknown", info); !strings.HasPrefix(got, "tclient 0.1.0 (commit source-commit-dirty, built unknown,") {
		t.Fatalf("go install/source fallback: %s", got)
	}
	if got := formatVersion("0.2.0", "release-commit", "2026-11-03T00:00:00Z", info); !strings.HasPrefix(got, "tclient 0.2.0 (commit release-commit, built 2026-11-03T00:00:00Z,") {
		t.Fatalf("release metadata lost precedence: %s", got)
	}
}
