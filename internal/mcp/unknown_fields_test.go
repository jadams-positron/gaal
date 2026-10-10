package mcp_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/getgaal/gaal/internal/config"
	"github.com/getgaal/gaal/internal/mcp"
	toml "github.com/pelletier/go-toml/v2"
)

var unknownFieldTargets = []struct {
	format, filename, serversKey, seed string
}{
	{
		format: "JSON", filename: "mcp.json", serversKey: "mcpServers",
		seed: `{"model":"keep","mcpServers":{
			"github":{"command":"old","args":["old"],"disabled":true,"cwd":"/work","envFile":".env","custom":{"id":9007199254740993,"values":[false,null,"x"]}},
			"retained":{"command":"keep","disabled":false,"custom":[]},
			"orphan":{"command":"remove","disabled":true}
		}}`,
	},
	{
		format: "TOML", filename: "config.toml", serversKey: "mcp_servers",
		seed: `model = "keep"
[mcp_servers.github]
command = "old"
args = ["old"]
enabled = false
startup_timeout_sec = 30
custom = { id = 9007199254740993, values = [false, "x"], date = 2026-01-02 }
[mcp_servers.retained]
command = "keep"
enabled = true
startup_timeout_sec = 1.5
[mcp_servers.orphan]
command = "remove"
enabled = false
`,
	},
}

func TestManager_PreservesUnknownServerFields(t *testing.T) {
	slog.DebugContext(t.Context(), "testing unknown mcp field preservation", "formats", len(unknownFieldTargets))
	for _, target := range unknownFieldTargets {
		for _, operation := range []string{"Sync_unrelated", "Sync_managed", "Prune"} {
			t.Run(target.format+"/"+operation, func(t *testing.T) {
				slog.DebugContext(t.Context(), "rewriting mcp config", "format", target.format, "operation", operation)
				path := filepath.Join(t.TempDir(), target.filename)
				if err := os.WriteFile(path, []byte(target.seed), 0o600); err != nil {
					t.Fatal(err)
				}
				want := readMCPDocument(t, path)
				servers := want[target.serversKey].(map[string]any)
				entries := []config.ConfigMcp{{
					Name: "other", Target: path, Inline: &config.ConfigMcpItem{Command: "new"},
				}}
				switch operation {
				case "Sync_unrelated":
					servers["other"] = map[string]any{"command": "new"}
				case "Sync_managed":
					entries[0].Name = "github"
					github := servers["github"].(map[string]any)
					github["command"] = "new"
					delete(github, "args")
				case "Prune":
					entries = []config.ConfigMcp{{Name: "github", Target: path}, {Name: "retained", Target: path}}
					delete(servers, "orphan")
				}
				manager := mcp.NewManager(entries, "", "")
				var err error
				if operation == "Prune" {
					err = manager.Prune(t.Context())
				} else {
					err = manager.Sync(t.Context())
				}
				if err != nil {
					t.Fatal(err)
				}
				if got := readMCPDocument(t, path); !reflect.DeepEqual(got, want) {
					t.Errorf("config fields changed:\ngot:  %#v\nwant: %#v", got, want)
				}
			})
		}
	}
}

func TestManager_Status_IgnoresUnknownServerFields(t *testing.T) {
	slog.DebugContext(t.Context(), "testing mcp drift with unknown fields", "formats", len(unknownFieldTargets))
	for _, target := range unknownFieldTargets {
		for _, tc := range []struct {
			name, command string
			dirty         bool
		}{
			{name: "extras_only", command: "old"},
			{name: "modeled_change", command: "new", dirty: true},
		} {
			t.Run(target.format+"/"+tc.name, func(t *testing.T) {
				slog.DebugContext(t.Context(), "checking mcp drift", "format", target.format, "case", tc.name)
				path := filepath.Join(t.TempDir(), target.filename)
				if err := os.WriteFile(path, []byte(target.seed), 0o600); err != nil {
					t.Fatal(err)
				}
				manager := mcp.NewManager([]config.ConfigMcp{{
					Name: "github", Target: path,
					Inline: &config.ConfigMcpItem{Command: tc.command, Args: []string{"old"}},
				}}, "", "")
				statuses := manager.Status(t.Context())
				if len(statuses) != 1 {
					t.Fatalf("got %d statuses, want 1", len(statuses))
				}
				if got := statuses[0]; got.Err != nil || !got.Present || got.Dirty != tc.dirty {
					t.Errorf("status = %+v, want present and dirty=%v", got, tc.dirty)
				}
			})
		}
	}
}

func TestManager_Sync_RespectsModeledFieldCase(t *testing.T) {
	slog.DebugContext(t.Context(), "testing modeled mcp field case", "formats", len(unknownFieldTargets))
	for _, target := range unknownFieldTargets {
		for _, key := range []string{"ARGS", "arg\u017f"} {
			t.Run(target.format+"/"+key, func(t *testing.T) {
				slog.DebugContext(t.Context(), "syncing alternate field spelling", "format", target.format, "key", key)
				path := filepath.Join(t.TempDir(), target.filename)
				seed := fmt.Sprintf(`{"mcpServers":{"s":{"command":"cmd",%q:["old"]}}}`, key)
				want := map[string]any{"command": "cmd"}
				if target.format == "TOML" {
					seed = fmt.Sprintf("[mcp_servers.s]\ncommand = 'cmd'\n%q = ['old']\n", key)
					want[key] = []any{"old"}
				}
				if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
					t.Fatal(err)
				}
				manager := mcp.NewManager([]config.ConfigMcp{{
					Name: "s", Target: path, Inline: &config.ConfigMcpItem{Command: "cmd"},
				}}, "", "")
				if err := manager.Sync(t.Context()); err != nil {
					t.Fatal(err)
				}
				doc := readMCPDocument(t, path)
				if got := doc[target.serversKey].(map[string]any)["s"]; !reflect.DeepEqual(got, want) {
					t.Errorf("server = %#v, want %#v", got, want)
				}
				statuses := manager.Status(t.Context())
				if len(statuses) != 1 || statuses[0].Err != nil || !statuses[0].Present || statuses[0].Dirty {
					t.Errorf("status after sync = %+v, want present and clean", statuses)
				}
			})
		}
	}
}

func readMCPDocument(t *testing.T, path string) map[string]any {
	t.Helper()
	slog.DebugContext(t.Context(), "reading mcp test config", "path", path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if filepath.Ext(path) == ".toml" {
		err = toml.Unmarshal(data, &doc)
	} else {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		err = decoder.Decode(&doc)
	}
	if err != nil {
		t.Fatal(err)
	}
	return doc
}
