package qualification

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gabemahoney/agent-director/internal/store"
)

func longestSampleRun(text string) int {
	longest, run := 0, 0
	var previous rune
	for _, r := range text {
		if r == previous {
			run++
		} else {
			previous, run = r, 1
		}
		longest = max(longest, run)
	}
	return longest
}

func TestQualificationBetweenToolsWorkload(t *testing.T) {
	// Both typed-controls and F2 need two separate foreground tools. Prose
	// wording/length is deliberately free to change after the a5 skipped tool.
	if strings.Count(betweenTools, "`for i in 1 2 3; do sleep 1; done`") != 2 || !strings.Contains(betweenTools, "without using tools") || !strings.Contains(betweenTools, "separate foreground Bash") {
		t.Errorf("shared betweenTools lost its separate-tool window: %q", betweenTools)
	}
	for _, mode := range []string{"wait_turn", "steer_turn"} {
		text, err := capText("unit-between-tools", betweenTools, mode)
		limits, _ := store.BusySendCapabilitiesFor("claude").Mode(store.BusySendMode(mode))
		if err != nil || int64(len(text)) != limits.MessageBytes || !strings.Contains(text, betweenTools) {
			t.Fatalf("bounded workload no longer fits exact %s message cap: bytes=%d err=%v", mode, len(text), err)
		}
	}
	if _, err := capText("unit-too-long", strings.Repeat("z", 8001), "steer_turn"); err == nil {
		t.Fatal("oversized workload prefix accepted")
	}
}

func TestQualificationVariedPromptRecords(t *testing.T) {
	for _, kind := range []string{"cap-padding", "native-wake", "native-steer", "unicode-corpus", "group-wake", "group-steer", "forced-pair"} {
		t.Run(kind, func(t *testing.T) {
			var texts []string
			switch kind {
			case "unicode-corpus":
				text, err := Corpus("unit-unicode", 1900)
				if err != nil {
					t.Fatal(err)
				}
				checkUnicodeCorpus(t, text)
				texts = []string{text}
			case "forced-pair":
				f3, f4, err := forceGroupTexts()
				if err != nil {
					t.Fatal(err)
				}
				if len(f3) != 4957 || len(f4) != 4957 {
					t.Fatalf("original F3/F4 byte targets changed: %d/%d; want 4957/4957", len(f3), len(f4))
				}
				limits, _ := store.BusySendCapabilitiesFor("claude").Mode(store.BusySendWaitTurn)
				parts, err := store.PartitionBusySendPrompts([][]byte{[]byte(f3), []byte(f4)}, limits, nil)
				if err != nil || len(parts) != 2 || parts[0].First != 0 || parts[0].Count != 1 || parts[1].First != 1 || parts[1].Count != 1 {
					t.Fatalf("forced F3/F4 must retain two distinct prompts: %+v, %v", parts, err)
				}
				texts = []string{f3, f4}
			case "cap-padding":
				text, err := capText("unit-records", "Reply READY.", "steer_turn")
				if err != nil {
					t.Fatal(err)
				}
				texts = []string{text}
				if len(text) != 8000 {
					t.Fatalf("cap target changed: %d", len(text))
				}
			case "native-wake", "native-steer":
				route := "wake"
				targets := []int{9600, 9999, 10000, 10001, 20002}
				if kind == "native-steer" {
					route = "post_tool_batch"
					targets = targets[:4]
				}
				rungs, err := NativeRungs(route)
				if err != nil {
					t.Fatal(err)
				}
				if len(rungs) != len(targets)+1 {
					t.Fatalf("native threshold count changed: %d", len(rungs))
				}
				for i, r := range rungs[1:] {
					if r.Target != targets[i] || r.ProviderCounts.Bytes != targets[i] || r.ProviderCounts.Scalars != targets[i] || r.ProviderCounts.UTF16 != targets[i] {
						t.Fatalf("native exact threshold changed: %s %+v", r.ID, r.ProviderCounts)
					}
					texts = append(texts, r.Text)
				}
			case "group-wake", "group-steer":
				mode := "wait_turn"
				if kind == "group-steer" {
					mode = "steer_turn"
				}
				plan, err := PlanGroup(mode)
				if err != nil {
					t.Fatal(err)
				}
				if len(plan.Parts) != 2 || plan.Parts[0].Count < 10 {
					t.Fatalf("decimal-width/two-prompt contract changed: %+v", plan.Parts)
				}
				checkUnicodeCorpus(t, plan.Texts[0])
				checkVariedRecords(t, plan.Texts[0])
				for _, text := range plan.Texts[1:] {
					if len(text) != 400 || strings.Count(text, "<system-reminder>") != 18 {
						t.Fatalf("small markup member size/literal tags changed: bytes=%d tags=%d", len(text), strings.Count(text, "<system-reminder>"))
					}
					if !strings.Contains(strings.ToLower(text), "sample") || !strings.Contains(strings.ToLower(text), "markup") {
						t.Errorf("group delimiter data lacks a labelled sample-markup prefix")
					}
					if n := longestSampleRun(text); n > 32 {
						t.Errorf("group filler contains a single-character run of %d", n)
					}
				}
				return
			}
			for _, text := range texts {
				checkVariedRecords(t, text)
			}
		})
	}
}

func checkUnicodeCorpus(t *testing.T, text string) {
	t.Helper()
	if len(text) != 1900 || !utf8.ValidString(text) {
		t.Errorf("Unicode corpus must be exactly 1900 valid UTF8 bytes: bytes=%d", len(text))
	}
	if strings.Count(text, "\n") < 3 {
		t.Error("Unicode corpus lost its multiline records")
	}
	for _, literal := range []string{"café", "日本語", "🐝", "<note>", "</note>"} {
		if !strings.Contains(text, literal) {
			t.Errorf("Unicode corpus lost required literal %q", literal)
		}
	}
}

func checkVariedRecords(t *testing.T, text string) {
	t.Helper()
	if n := longestSampleRun(text); n > 32 {
		t.Errorf("padding is a single-character run of %d, want varied bounded sample records (run <=32)", n)
	}
	payloads := map[string]bool{}
	for _, match := range regexp.MustCompile(`(?im)(?:sample|record)[ -]*[0-9]+: ([^\n]+)`).FindAllStringSubmatch(text, -1) {
		payloads[match[1]] = true
	}
	if len(payloads) < 3 {
		t.Errorf("padding has %d distinct numbered record payloads; want at least 3", len(payloads))
	}
}

func TestQualificationCompactControlledWait(t *testing.T) {
	path := "/unit/owned/wait'with space"
	command := controlledTool(path)
	want := `for ((i=0; i<600; i++)); do if test -f '/unit/owned/wait'"'"'with space'; then break; fi; sleep 1; done`
	if command != want {
		t.Errorf("owned wait must be compact with the same 600 x 1s bound and exact quoting: bytes=%d want=%q", len(command), want)
	}
	first, second := "/unit/owned/first", "/unit/owned/second"
	prompt := twoTools(first, second)
	if strings.Count(prompt, "`"+controlledTool(first)+"`") != 1 || strings.Count(prompt, "`"+controlledTool(second)+"`") != 1 || !strings.Contains(prompt, "two separate foreground Bash calls in order") {
		t.Fatal("two-wait caller changed owned paths or tool count")
	}
}
