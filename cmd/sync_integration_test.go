package cmd_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getgaal/gaal/cmd"
	"github.com/getgaal/gaal/internal/config/platform"
)

func TestOneShotSync(t *testing.T) {
	slog.Debug("testing one-shot sync", "process", os.Getenv("GAAL_SYNC_TEST_PROCESS"))
	if os.Getenv("GAAL_SYNC_TEST_PROCESS") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{"gaal"}, os.Args[i+1:]...)
				cmd.Execute()
				os.Exit(0)
			}
		}
		t.Fatal("missing command arguments")
	}

	t.Setenv("PROGRAMDATA", t.TempDir())
	if _, err := os.Stat(platform.GlobalConfigFilePath()); !os.IsNotExist(err) {
		t.Skip("CLI loads system configuration outside the sandbox")
	}

	for _, tc := range []struct {
		name    string
		verbose bool
		want    string
	}{
		{"brief reports post-sync skill count", false, "claude-code: 1 skill"},
		{"verbose scans filesystem once", true, "installed in claude-code"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			slog.Debug("testing sync output", "case", tc.name)
			dir := t.TempDir()
			source := filepath.Join(dir, "source", "demo")
			if err := os.MkdirAll(source, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(source, "SKILL.md"), []byte("---\nname: demo\n---\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg := filepath.Join(dir, "gaal.yaml")
			data := fmt.Sprintf("schema: 1\nskills:\n  - source: %q\n    agents: [claude-code]\n    global: true\n", filepath.ToSlash(source))
			if err := os.WriteFile(cfg, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			home := filepath.Join(dir, "home")
			args := []string{"-test.run=^TestOneShotSync$", "--", "--no-banner", "--sandbox", home, "-c", cfg, "sync"}
			if tc.verbose {
				args = append(args, "-v")
			}
			process := exec.Command(exe, args...)
			process.Dir = dir
			process.Env = append(os.Environ(), "GAAL_SYNC_TEST_PROCESS=1", "GAAL_TELEMETRY=0",
				"HOME="+home, "USERPROFILE="+home,
				"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
				"APPDATA="+filepath.Join(home, "AppData", "Roaming"))
			var stdout, stderr bytes.Buffer
			process.Stdout, process.Stderr = &stdout, &stderr
			if err := process.Run(); err != nil {
				t.Fatalf("sync: %v\n%s\n%s", err, &stdout, &stderr)
			}
			if !strings.Contains(stdout.String(), tc.want) {
				t.Errorf("sync output missing %q:\n%s", tc.want, &stdout)
			}
			if tc.verbose {
				// Requirement: one-shot sync performs only one filesystem discovery scan.
				if got := strings.Count(stderr.String(), "scanning filesystem"); got != 1 {
					t.Errorf("filesystem discovery scans = %d, want 1", got)
				}
			}
		})
	}
}
