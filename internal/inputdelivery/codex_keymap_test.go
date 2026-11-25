package inputdelivery

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestCodexDefaultKeymapConfig01592(t *testing.T) {
	// b.ab7: runtime defaults include Insert, which 0.159.2 config rejects.
	// Decode the -c RHS as an inline TOML value, using the existing dependency.
	value, ok := strings.CutPrefix(codexDefaultKeymap, "tui.keymap=")
	if !ok {
		t.Fatal("managed keymap must be a tui.keymap override")
	}
	var config struct {
		Keymap map[string]map[string]any
	}
	if _, err := toml.Decode("keymap="+value, &config); err != nil {
		t.Fatalf("parse managed keymap inline table: %v", err)
	}

	// Independent field inventory from openai/codex tag rust-v0.159.2,
	// codex-rs/config/src/tui_keymap.rs: TuiKeymap and its Tui*Keymap structs.
	// Pin every leaf, including empty bindings, to prevent lower-layer merging.
	fields := map[string]string{
		"global":          "open_agents open_transcript find_transcript focus_activity open_external_editor copy clear_terminal submit queue toggle_shortcuts toggle_vim_mode toggle_fast_mode toggle_raw_output toggle_side_conversation",
		"chat":            "toggle_voice toggle_voice_mute interrupt_turn decrease_reasoning_effort increase_reasoning_effort previous_permission_mode next_permission_mode edit_queued_message prompt_stack_back skip_question",
		"composer":        "submit queue toggle_shortcuts history_search_previous history_search_next",
		"editor":          "insert_newline move_left move_right move_up move_down move_word_left move_word_right move_line_start move_line_end delete_backward delete_forward delete_backward_word delete_forward_word kill_line_start kill_whole_line kill_line_end yank",
		"vim_normal":      "enter_insert append_after_cursor append_line_end insert_line_start open_line_below open_line_above enter_replace_mode move_left move_right move_up move_down move_word_forward move_word_backward move_word_end move_line_start move_line_end find_forward find_backward till_forward till_backward jump_top jump_bottom delete_char replace_char repeat_last_change substitute_char delete_to_line_end change_to_line_end yank_line paste_after start_delete_operator start_yank_operator start_change_operator undo redo cancel_operator",
		"vim_operator":    "delete_line yank_line motion_left motion_right motion_up motion_down motion_word_forward motion_word_backward motion_word_end motion_line_start motion_line_end motion_find_forward motion_find_backward motion_till_forward motion_till_backward motion_jump_top motion_jump_bottom select_inner_text_object select_around_text_object cancel",
		"vim_search":      "forward backward next previous",
		"vim_text_object": "word big_word parentheses brackets braces double_quote single_quote backtick cancel",
		"pager":           "scroll_up scroll_down page_up page_down half_page_up half_page_down jump_top jump_bottom close close_transcript find",
		"list":            "move_up move_down move_left move_right page_up page_down jump_top jump_bottom accept cancel",
		"agents":          "resume search new_task new_worktree rename stop archive delete hide toggle_grouping",
		"approval":        "open_fullscreen open_thread approve approve_for_session approve_for_prefix deny decline cancel",
	}
	for context, names := range fields {
		actions, ok := config.Keymap[context]
		if !ok {
			t.Errorf("missing context tui.keymap.%s", context)
			continue
		}
		for _, action := range strings.Fields(names) {
			if _, ok := actions[action]; !ok {
				t.Errorf("missing action tui.keymap.%s.%s", context, action)
			}
		}
	}
	for context, actions := range config.Keymap {
		names, ok := fields[context]
		if !ok {
			t.Errorf("unknown context tui.keymap.%s", context)
			continue
		}
		for action, value := range actions {
			path := "tui.keymap." + context + "." + action
			if !strings.Contains(" "+names+" ", " "+action+" ") {
				t.Errorf("unknown action %s", path)
			}
			// KeybindingsSpec accepts One(string) or Many(array of strings).
			var bindings []any
			switch value := value.(type) {
			case string:
				bindings = []any{value}
			case []any:
				bindings = value
			default:
				t.Errorf("%s: expected string or array of strings, got %T", path, value)
				continue
			}
			for _, binding := range bindings {
				spec, ok := binding.(string)
				if !ok {
					t.Errorf("%s: expected string binding, got %T", path, binding)
					continue
				}
				if err := validateCodex01592Keybinding(spec); err != nil {
					t.Errorf("%s: binding %q rejected by Codex 0.159.2: %v", path, spec, err)
				}
			}
		}
	}
}

// Validation port of normalize_keybinding_spec, normalize_keybinding_stroke,
// and normalize_key_name in openai/codex rust-v0.159.2,
// codex-rs/config/src/tui_keymap.rs:538-691 (also keymap-runner.py's norm_spec).
// Only acceptance matters here; canonical output and error wording are omitted.
func validateCodex01592Keybinding(raw string) error {
	strokes := strings.Fields(raw)
	if len(strokes) == 0 {
		strokes = []string{raw}
	}
	if len(strokes) > 2 {
		return fmt.Errorf("too many strokes")
	}
	for _, stroke := range strokes {
		if err := validateCodex01592Stroke(stroke); err != nil {
			return err
		}
	}
	return nil
}

func validateCodex01592Stroke(raw string) error {
	lower := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, strings.TrimSpace(raw))
	if lower == "" {
		return fmt.Errorf("empty keybinding")
	}
	modifiers := map[string]string{"ctrl": "ctrl", "control": "ctrl", "alt": "alt", "option": "alt", "shift": "shift"}
	seen := map[string]bool{}
	var keys []string
	for _, segment := range strings.Split(lower, "-") {
		if segment == "" {
			continue
		}
		modifier, isModifier := modifiers[segment]
		if len(keys) == 0 && isModifier {
			if seen[modifier] {
				return fmt.Errorf("duplicate modifier")
			}
			seen[modifier] = true
			continue
		}
		keys = append(keys, segment)
	}
	if len(keys) == 0 {
		return fmt.Errorf("missing key")
	}
	for _, key := range keys {
		if _, ok := modifiers[key]; ok {
			return fmt.Errorf("modifier after key")
		}
	}
	key := strings.Join(keys, "-")
	aliases := map[string]string{"escape": "esc", "return": "enter", "spacebar": "space", "pgup": "page-up", "pageup": "page-up", "pgdn": "page-down", "pagedown": "page-down", "del": "delete"}
	if alias, ok := aliases[key]; ok {
		key = alias
	}
	if len(key) == 1 && key[0] >= 32 && key[0] < 127 && key != "-" {
		return nil
	}
	switch key {
	case "enter", "tab", "backspace", "esc", "delete", "up", "down", "left", "right", "home", "end", "page-up", "page-down", "space", "minus":
		return nil
	}
	if number, ok := strings.CutPrefix(key, "f"); ok {
		// Rust's u8 parser accepts an optional leading plus.
		n, err := strconv.ParseUint(strings.TrimPrefix(number, "+"), 10, 8)
		if err == nil && n >= 1 && n <= 24 {
			return nil
		}
	}
	return fmt.Errorf("unknown key %q", key)
}
