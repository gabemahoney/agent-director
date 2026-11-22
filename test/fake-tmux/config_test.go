package main

import (
	"reflect"
	"testing"
)

// b.xhh: decode only the producer's small grammar, preserving token bytes;
// reject shell/tmux syntax this fixture does not promise to implement.
func TestConfigCommandsGrammar(t *testing.T) {
	input := "'new-session' '' 'a'\\047'b'\\012''\\011''\\001''\\177'雪\\;#{x}' ';' ; 'set-option' '@ad_owner' 'value'\n"
	want := [][]string{{"new-session", "", "a'b\n\t\x01\x7f雪\\;#{x}", ";"}, {"set-option", "@ad_owner", "value"}}
	if got, err := configCommands(input); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("config decode = %q, %v; want %q", got, err, want)
	}
	for _, bad := range []string{"'new-session'", "new-session\n", "'new-session' 'unterminated\n", "'new-session' \"expanded\"\n", "'new-session' 'literal\nnewline'\n", "'new-session' 'a'\\000'b'\n", "'new-session' 'a'\\888'b'\n", "'new-session' 'a'\\141'b'\n", "'new-session' ; \n", "'new-session' ; 'kill-server'\n"} {
		if _, err := configCommands(bad); err == nil {
			t.Errorf("unsupported config accepted: %q", bad)
		}
	}
}
