package main

import (
	"reflect"
	"strings"
	"testing"
)

// TestParseGlobalFlags covers the three global flags introduced for b.32k:
//   --store-path / --home / --tmux-command
//
// Each subtest verifies (a) the parsed values land on the right field, (b) the
// `*Set` sentinels go true, and (c) flag tokens are stripped from the returned
// argv so per-verb FlagSets see the verb args unchanged.
func TestParseGlobalFlags(t *testing.T) {
	cases := []struct {
		name        string
		argv        []string
		wantOpts    globalOptions
		wantStrippedArgv []string
	}{
		{
			name:             "empty argv → zero opts, empty stripped",
			argv:             []string{},
			wantOpts:         globalOptions{},
			wantStrippedArgv: []string{},
		},
		{
			name: "store-path two-token form",
			argv: []string{"--store-path", "/tmp/foo.db", "version"},
			wantOpts: globalOptions{
				storePath:    "/tmp/foo.db",
				storePathSet: true,
			},
			wantStrippedArgv: []string{"version"},
		},
		{
			name: "store-path equals form",
			argv: []string{"--store-path=/tmp/foo.db", "version"},
			wantOpts: globalOptions{
				storePath:    "/tmp/foo.db",
				storePathSet: true,
			},
			wantStrippedArgv: []string{"version"},
		},
		{
			name: "home two-token form",
			argv: []string{"--home", "/tmp/h", "help"},
			wantOpts: globalOptions{
				home:    "/tmp/h",
				homeSet: true,
			},
			wantStrippedArgv: []string{"help"},
		},
		{
			name: "home equals form",
			argv: []string{"--home=/tmp/h", "help"},
			wantOpts: globalOptions{
				home:    "/tmp/h",
				homeSet: true,
			},
			wantStrippedArgv: []string{"help"},
		},
		{
			name: "tmux-command two-token form",
			argv: []string{"--tmux-command", "/usr/local/bin/tmux", "spawn", "--cwd", "/x"},
			wantOpts: globalOptions{
				tmuxCommand:    "/usr/local/bin/tmux",
				tmuxCommandSet: true,
			},
			wantStrippedArgv: []string{"spawn", "--cwd", "/x"},
		},
		{
			name: "tmux-command equals form",
			argv: []string{"--tmux-command=/usr/local/bin/tmux", "spawn", "--cwd", "/x"},
			wantOpts: globalOptions{
				tmuxCommand:    "/usr/local/bin/tmux",
				tmuxCommandSet: true,
			},
			wantStrippedArgv: []string{"spawn", "--cwd", "/x"},
		},
		{
			name: "all three together (mixed forms)",
			argv: []string{
				"--store-path", "/tmp/foo.db",
				"--home=/tmp/h",
				"--tmux-command", "/usr/local/bin/tmux",
				"version",
			},
			wantOpts: globalOptions{
				storePath:      "/tmp/foo.db",
				storePathSet:   true,
				home:           "/tmp/h",
				homeSet:        true,
				tmuxCommand:    "/usr/local/bin/tmux",
				tmuxCommandSet: true,
			},
			wantStrippedArgv: []string{"version"},
		},
		{
			name: "unknown flags pass through untouched",
			argv: []string{"spawn", "--cwd", "/x", "--label", "k=v"},
			wantOpts: globalOptions{},
			wantStrippedArgv: []string{"spawn", "--cwd", "/x", "--label", "k=v"},
		},
		{
			name: "global flag interleaved with verb args",
			argv: []string{"--store-path=/tmp/foo.db", "spawn", "--cwd", "/x"},
			wantOpts: globalOptions{
				storePath:    "/tmp/foo.db",
				storePathSet: true,
			},
			wantStrippedArgv: []string{"spawn", "--cwd", "/x"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOpts, gotStrippedArgv, err := parseGlobalFlags(tc.argv)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(gotOpts, tc.wantOpts) {
				t.Errorf("opts mismatch:\n got %+v\nwant %+v", gotOpts, tc.wantOpts)
			}
			if !reflect.DeepEqual(gotStrippedArgv, tc.wantStrippedArgv) {
				t.Errorf("stripped argv mismatch:\n got %q\nwant %q", gotStrippedArgv, tc.wantStrippedArgv)
			}
		})
	}
}

// TestParseGlobalFlags_MissingValue covers the error path for both flag forms:
//   - the two-token form when a recognized flag sits at the tail of argv with
//     no following value (`--store-path` with nothing after it), and
//   - the `=` form with an empty value (`--store-path=`), which previously
//     fell through silently and behaved identically to omitting the flag.
//
// All three global flags must reject both shapes the same way so users get a
// consistent error instead of a footgun where their override silently no-ops.
func TestParseGlobalFlags_MissingValue(t *testing.T) {
	cases := [][]string{
		// Two-token form, flag at tail of argv.
		{"--store-path"},
		{"--home"},
		{"--tmux-command"},
		{"spawn", "--store-path"},
		// `=` form with empty value — must error the same way.
		{"--store-path="},
		{"--home="},
		{"--tmux-command="},
		{"--store-path=", "version"},
	}
	for _, argv := range cases {
		// Join the full argv so each subtest name is unique (the previous
		// `argv[len(argv)-1]` scheme produced duplicates like "--store-path"
		// across multiple cases).
		t.Run(strings.Join(argv, "_"), func(t *testing.T) {
			_, _, err := parseGlobalFlags(argv)
			if err == nil {
				t.Errorf("argv=%q expected error, got nil", argv)
			}
		})
	}
}
