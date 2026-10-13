package spawn

import (
	"reflect"
	"slices"
	"testing"
)

// Expectations follow the actual 2.1.280/2.1.296 parser traces in b.ssz's probe.
func TestReadClaudeArgsOwnership(t *testing.T) {
	for _, tc := range []struct {
		name        string
		args        []string
		options     []ArgOption
		positionals []int
		delimiter   int
	}{
		{"required short-shaped value", []string{"--model", "-c", "prompt"}, []ArgOption{{Index: 0, Name: "--model", Canonical: "--model", Spelling: "--model", Values: []string{"-c"}, ValueIndices: []int{1}, Known: true}}, []int{2}, -1},
		{"required prose starting dash", []string{"--append-system-prompt", "-concise answers"}, []ArgOption{{Name: "--append-system-prompt", Canonical: "--append-system-prompt", Spelling: "--append-system-prompt", Values: []string{"-concise answers"}, ValueIndices: []int{1}, Known: true}}, nil, -1},
		{"owned exact denied long", []string{"--append-system-prompt", "--resume", "foo"}, []ArgOption{{Name: "--append-system-prompt", Canonical: "--append-system-prompt", Spelling: "--append-system-prompt", Values: []string{"--resume"}, ValueIndices: []int{1}, Known: true}}, []int{2}, -1},
		{"required value owns delimiter", []string{"--model", "--", "-c"}, []ArgOption{{Name: "--model", Canonical: "--model", Spelling: "--model", Values: []string{"--"}, ValueIndices: []int{1}, Known: true}, {Index: 2, Name: "-c", Canonical: "--continue", Spelling: "-c", Known: true}}, nil, -1},
		{"delimiter makes selectors operands", []string{"--", "--resume", "-p"}, nil, []int{1, 2}, 0},
		{"optional detached value", []string{"-r", "S1", "prompt"}, []ArgOption{{Name: "-r", Canonical: "--resume", Spelling: "-r", Values: []string{"S1"}, ValueIndices: []int{1}, Known: true}}, []int{2}, -1},
		{"optional stops at flag", []string{"--resume", "-c"}, []ArgOption{{Name: "--resume", Canonical: "--resume", Spelling: "--resume", Known: true}, {Index: 1, Name: "-c", Canonical: "--continue", Spelling: "-c", Known: true}}, nil, -1},
		{"optional owns lone dash", []string{"--resume", "-"}, []ArgOption{{Name: "--resume", Canonical: "--resume", Spelling: "--resume", Values: []string{"-"}, ValueIndices: []int{1}, Known: true}}, nil, -1},
		{"optional long equals", []string{"--resume=S1"}, []ArgOption{{Name: "--resume", Canonical: "--resume", Spelling: "--resume=S1", Values: []string{"S1"}, Known: true}}, nil, -1},
		{"optional attached", []string{"-rS1"}, []ArgOption{{Name: "-r", Canonical: "--resume", Spelling: "-rS1", Values: []string{"S1"}, ClusterStart: 1, ClusterEnd: 4, Known: true}}, nil, -1},
		{"short equals retained", []string{"-r=S1"}, []ArgOption{{Name: "-r", Canonical: "--resume", Spelling: "-r=S1", Values: []string{"=S1"}, ClusterStart: 1, ClusterEnd: 5, Known: true}}, nil, -1},
		{"debug consumes cluster suffix", []string{"-dc"}, []ArgOption{{Name: "-d", Canonical: "--debug", Spelling: "-dc", Values: []string{"c"}, ClusterStart: 1, ClusterEnd: 3, Known: true}}, nil, -1},
		{"continue then optional debug", []string{"-cd"}, []ArgOption{{Name: "-c", Canonical: "--continue", Spelling: "-cd", ClusterStart: 1, ClusterEnd: 2, Known: true}, {Name: "-d", Canonical: "--debug", Spelling: "-cd", ClusterStart: 2, ClusterEnd: 3, Known: true}}, nil, -1},
		{"print and continue cluster", []string{"-pc"}, []ArgOption{{Name: "-p", Canonical: "--print", Spelling: "-pc", ClusterStart: 1, ClusterEnd: 2, Known: true}, {Name: "-c", Canonical: "--continue", Spelling: "-pc", ClusterStart: 2, ClusterEnd: 3, Known: true}}, nil, -1},
		{"continue debug suffix avoids exact short", []string{"-cd2e"}, []ArgOption{{Name: "-c", Canonical: "--continue", Spelling: "-cd2e", ClusterStart: 1, ClusterEnd: 2, Known: true}, {Name: "-d", Canonical: "--debug", Spelling: "-cd2e", Values: []string{"2e"}, ClusterStart: 2, ClusterEnd: 5, Known: true}}, nil, -1},
		{"print debug suffix avoids exact short", []string{"-pd2e"}, []ArgOption{{Name: "-p", Canonical: "--print", Spelling: "-pd2e", ClusterStart: 1, ClusterEnd: 2, Known: true}, {Name: "-d", Canonical: "--debug", Spelling: "-pd2e", Values: []string{"2e"}, ClusterStart: 2, ClusterEnd: 5, Known: true}}, nil, -1},
		{"exact short before clusters", []string{"-d2e", "prompt"}, []ArgOption{{Name: "-d2e", Canonical: "--debug-to-stderr", Spelling: "-d2e", Known: true}}, []int{1}, -1},
		{"required attached name", []string{"-nrocket"}, []ArgOption{{Name: "-n", Canonical: "--name", Spelling: "-nrocket", Values: []string{"rocket"}, ClusterStart: 1, ClusterEnd: 8, Known: true}}, nil, -1},
		{"required short equals retained", []string{"-n=rocket"}, []ArgOption{{Name: "-n", Canonical: "--name", Spelling: "-n=rocket", Values: []string{"=rocket"}, ClusterStart: 1, ClusterEnd: 9, Known: true}}, nil, -1},
		{"required long equals", []string{"--model=-c"}, []ArgOption{{Name: "--model", Canonical: "--model", Spelling: "--model=-c", Values: []string{"-c"}, Known: true}}, nil, -1},
		{"required missing value", []string{"--model"}, []ArgOption{{Name: "--model", Canonical: "--model", Spelling: "--model", Known: true}}, nil, -1},
		{"required list first value can be flag", []string{"--allowed-tools", "--resume", "Read", "Write", "-c"}, []ArgOption{{Name: "--allowed-tools", Canonical: "--allowedTools", Spelling: "--allowed-tools", Values: []string{"--resume", "Read", "Write"}, ValueIndices: []int{1, 2, 3}, Known: true}, {Index: 4, Name: "-c", Canonical: "--continue", Spelling: "-c", Known: true}}, nil, -1},
		{"list delimiter boundary", []string{"--mcp-config", "a", "b", "--", "-c"}, []ArgOption{{Name: "--mcp-config", Canonical: "--mcp-config", Spelling: "--mcp-config", Values: []string{"a", "b"}, ValueIndices: []int{1, 2}, Known: true}}, []int{4}, 3},
		{"equals list does not consume next", []string{"--tools=Read", "prompt"}, []ArgOption{{Name: "--tools", Canonical: "--tools", Spelling: "--tools=Read", Values: []string{"Read"}, Known: true}}, []int{1}, -1},
		{"required list owns first delimiter", []string{"--tools", "--", "Read", "-c"}, []ArgOption{{Name: "--tools", Canonical: "--tools", Spelling: "--tools", Values: []string{"--", "Read"}, ValueIndices: []int{1, 2}, Known: true}, {Index: 3, Name: "-c", Canonical: "--continue", Spelling: "-c", Known: true}}, nil, -1},
		{"unknown long is valueless AD policy", []string{"--future", "prompt", "-c"}, []ArgOption{{Name: "--future", Canonical: "--future", Spelling: "--future"}, {Index: 2, Name: "-c", Canonical: "--continue", Spelling: "-c", Known: true}}, []int{1}, -1},
		{"unknown short remainder stays intact", []string{"-xc", "-c"}, []ArgOption{{Name: "-xc", Canonical: "-xc", Spelling: "-xc", ClusterStart: 1, ClusterEnd: 3}, {Index: 1, Name: "-c", Canonical: "--continue", Spelling: "-c", Known: true}}, nil, -1},
		{"value label is not alias", []string{"-message-id", "--resume-drops-turn", "turn"}, []ArgOption{{Name: "-message-id", Canonical: "-message-id", Spelling: "-message-id", ClusterStart: 1, ClusterEnd: 11}, {Index: 1, Name: "--resume-drops-turn", Canonical: "--resume-drops-turn", Spelling: "--resume-drops-turn", Values: []string{"turn"}, ValueIndices: []int{2}, Known: true}}, nil, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ReadClaudeArgs(tc.args)
			if !reflect.DeepEqual(got.Options, tc.options) || !slices.Equal(got.Positionals, tc.positionals) || got.DoubleDash != tc.delimiter {
				t.Fatalf("ReadClaudeArgs(%q) = %+v; want options %+v, positions %v, delimiter %d", tc.args, got, tc.options, tc.positionals, tc.delimiter)
			}
		})
	}
}

func TestClaudeHasPrintBeforeExtraction(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"resume first", []string{"--resume", "OLD", "--print"}, true},
		{"continue first", []string{"-c", "-p"}, true},
		{"cluster", []string{"-pc"}, true},
		{"print then resume", []string{"--print", "--resume=OLD"}, true},
		{"optional debug owns p", []string{"-dp"}, false},
		{"required value owns print", []string{"--append-system-prompt", "--print"}, false},
		{"prompt after delimiter", []string{"--", "--print", "-p"}, false},
		{"unknown cluster", []string{"-xp"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ReadClaudeArgs(tc.args).HasPrint(); got != tc.want {
				t.Fatalf("HasPrint(%q) = %v; want %v", tc.args, got, tc.want)
			}
		})
	}
}
