package inputdelivery

import (
	"os"
	"runtime"

	"github.com/BurntSushi/toml"
)

// codexDefaultKeymap explicitly supplies every action in Codex 0.159.2's
// TuiKeymap schema. Empty tables merge with lower layers, so every leaf must be
// present to prevent user, project, or cloud bindings from changing text/Enter.
// Source: openai/codex rust-v0.159.2, config/src/tui_keymap.rs and
// tui/src/keymap.rs (built_in_defaults). Vim starts disabled for this protocol.
const codexDefaultKeymap = "tui.keymap={" +
	"global={open_agents=[],open_transcript=[\"ctrl-t\"],find_transcript=[\"f3\"],focus_activity=[\"f4\"],open_external_editor=[\"ctrl-g\"],copy=[\"ctrl-o\"],clear_terminal=[\"ctrl-l\"],submit=[\"enter\"],queue=[\"tab\"],toggle_shortcuts=[\"?\",\"shift-?\"],toggle_vim_mode=[],toggle_fast_mode=[],toggle_raw_output=[\"alt-r\"],toggle_side_conversation=[\"ctrl-/\"]}," +
	"chat={toggle_voice=[\"f8\"],toggle_voice_mute=[\"ctrl-x\"],interrupt_turn=[\"esc\"],decrease_reasoning_effort=[\"alt-,\",\"shift-down\"],increase_reasoning_effort=[\"alt-.\",\"shift-up\"],previous_permission_mode=[],next_permission_mode=[],edit_queued_message=[\"shift-left\",\"alt-up\"],prompt_stack_back=[\"shift-right\",\"alt-down\"],skip_question=[\"ctrl-]\"]}," +
	"composer={submit=[\"enter\"],queue=[\"tab\"],toggle_shortcuts=[\"?\",\"shift-?\"],history_search_previous=[\"ctrl-r\"],history_search_next=[\"ctrl-s\"]}," +
	"editor={insert_newline=[\"ctrl-j\",\"ctrl-m\",\"enter\",\"shift-enter\",\"alt-enter\"],move_left=[\"left\",\"ctrl-b\"],move_right=[\"right\",\"ctrl-f\"],move_up=[\"up\",\"ctrl-p\"],move_down=[\"down\",\"ctrl-n\"],move_word_left=[\"alt-b\",\"alt-left\",\"ctrl-left\"],move_word_right=[\"alt-f\",\"alt-right\",\"ctrl-right\"],move_line_start=[\"home\",\"ctrl-a\"],move_line_end=[\"end\",\"ctrl-e\"],delete_backward=[\"backspace\",\"shift-backspace\",\"ctrl-h\"],delete_forward=[\"delete\",\"shift-delete\",\"ctrl-d\"],delete_backward_word=[\"alt-backspace\",\"ctrl-backspace\",\"ctrl-shift-backspace\",\"ctrl-w\",\"ctrl-alt-h\"],delete_forward_word=[\"alt-delete\",\"ctrl-delete\",\"ctrl-shift-delete\",\"alt-d\"],kill_line_start=[\"ctrl-u\"],kill_whole_line=[],kill_line_end=[\"ctrl-k\"],yank=[\"ctrl-y\"]}," +
	"vim_normal={enter_insert=[\"i\",\"insert\"],append_after_cursor=[\"a\"],append_line_end=[\"shift-a\"],insert_line_start=[\"shift-i\"],open_line_below=[\"o\"],open_line_above=[\"shift-o\"],enter_replace_mode=[\"shift-r\"],move_left=[\"h\",\"left\"],move_right=[\"l\",\"right\"],move_up=[\"k\",\"up\"],move_down=[\"j\",\"down\"],move_word_forward=[\"w\"],move_word_backward=[\"b\"],move_word_end=[\"e\"],move_line_start=[\"0\"],move_line_end=[\"$\",\"shift-$\"],find_forward=[\"f\"],find_backward=[\"shift-f\"],till_forward=[\"t\"],till_backward=[\"shift-t\"],jump_top=[],jump_bottom=[\"shift-g\"],delete_char=[\"x\"],replace_char=[\"r\"],repeat_last_change=[\".\"],substitute_char=[\"s\"],delete_to_line_end=[\"shift-d\"],change_to_line_end=[\"shift-c\"],yank_line=[\"shift-y\"],paste_after=[\"p\"],start_delete_operator=[\"d\"],start_yank_operator=[\"y\"],start_change_operator=[\"c\"],undo=[\"u\"],redo=[\"ctrl-r\"],cancel_operator=[\"esc\"]}," +
	"vim_operator={delete_line=[\"d\"],yank_line=[\"y\"],motion_left=[\"h\"],motion_right=[\"l\"],motion_up=[\"k\"],motion_down=[\"j\"],motion_word_forward=[\"w\"],motion_word_backward=[\"b\"],motion_word_end=[\"e\"],motion_line_start=[\"0\"],motion_line_end=[\"$\",\"shift-$\"],motion_find_forward=[\"f\"],motion_find_backward=[\"shift-f\"],motion_till_forward=[\"t\"],motion_till_backward=[\"shift-t\"],motion_jump_top=[],motion_jump_bottom=[\"shift-g\"],select_inner_text_object=[\"i\"],select_around_text_object=[\"a\"],cancel=[\"esc\"]}," +
	"vim_search={forward=[\"/\"],backward=[\"?\"],next=[\"n\"],previous=[\"shift-n\"]}," +
	"vim_text_object={word=[\"w\"],big_word=[\"shift-w\"],parentheses=[\"(\",\"shift-(\",\")\",\"shift-)\",\"b\"],brackets=[\"[\",\"]\"],braces=[\"{\",\"shift-{\",\"}\",\"shift-}\",\"shift-b\"],double_quote=[\"\\\"\",\"shift-\\\"\"],single_quote=[\"'\"],backtick=[\"`\"],cancel=[\"esc\"]}," +
	"pager={scroll_up=[\"up\",\"k\"],scroll_down=[\"down\",\"j\"],page_up=[\"page-up\",\"shift-space\",\"ctrl-b\"],page_down=[\"page-down\",\"space\",\"ctrl-f\"],half_page_up=[\"ctrl-u\"],half_page_down=[\"ctrl-d\"],jump_top=[\"home\"],jump_bottom=[\"end\"],close=[\"q\",\"ctrl-c\"],close_transcript=[\"ctrl-t\"],find=[\"f3\",\"/\"]}," +
	"list={move_up=[\"up\",\"ctrl-p\",\"ctrl-k\",\"k\"],move_down=[\"down\",\"ctrl-n\",\"ctrl-j\",\"j\"],move_left=[\"left\",\"ctrl-h\"],move_right=[\"right\",\"ctrl-l\"],page_up=[\"page-up\",\"ctrl-b\"],page_down=[\"page-down\",\"ctrl-f\"],jump_top=[\"home\"],jump_bottom=[\"end\"],accept=[\"enter\"],cancel=[\"esc\"]}," +
	"agents={resume=[\"o\"],search=[\"f\"],new_task=[\"n\"],new_worktree=[\"w\"],rename=[\"r\"],stop=[\"x\"],archive=[\"a\"],delete=[\"backspace\"],hide=[\"h\"],toggle_grouping=[\"g\"]}," +
	"approval={open_fullscreen=[\"ctrl-a\",\"ctrl-shift-a\"],open_thread=[\"o\"],approve=[\"y\"],approve_for_session=[\"a\"],approve_for_prefix=[\"p\"],deny=[\"d\"],decline=[\"esc\",\"n\"],cancel=[\"c\"]}}"

// Legacy managed settings have higher precedence than CLI settings. Platforms
// with additional native managed sources are unsupported by this Linux adapter.
func codexManagedComposerSafe() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	data, err := readBounded("/etc/codex/managed_config.toml", 1<<20)
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	var config map[string]any
	if _, err := toml.Decode(string(data), &config); err != nil {
		return false
	}
	return !hasComposerSettings(config)
}

func hasComposerSettings(config map[string]any) bool {
	for key, value := range config {
		child, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if key == "tui" {
			if _, ok := child["keymap"]; ok {
				return true
			}
			if _, ok := child["vim_mode_default"]; ok {
				return true
			}
		}
		if hasComposerSettings(child) {
			return true
		}
	}
	return false
}
