package main

import (
	"strings"
	"testing"
)

// Gateway sentinels for the results scrub: a URL with user info, a port and
// a long path part, and a token.
const (
	scrubURL   = "https://user:pw@Gateway.Example.invalid:8443/v1/tenant-0123456789xyz/"
	scrubToken = "tok-0123456789abcdefghij"
)

// gatewayScrubber scrubs scrubURL and scrubToken.
func gatewayScrubber() scrubber {
	return newScrubber(fakeEnv(map[string]string{"ANTHROPIC_BASE_URL": scrubURL, "ANTHROPIC_AUTH_TOKEN": scrubToken}, "/h"))
}

func TestScrubParts(t *testing.T) {
	scr := gatewayScrubber()
	for _, tc := range []struct{ name, in, want string }{
		{"the host", "host gateway.example.invalid here", "host <redacted> here"},
		{"the host with its port, in another case", "dial GATEWAY.EXAMPLE.INVALID:8443 failed", "dial <redacted> failed"},
		{"the URL without its trailing slash", "base " + strings.TrimSuffix(scrubURL, "/") + " ok", "base <redacted> ok"},
		{"a path part of 12 or more characters", "path /tenant-0123456789xyz/models", "path /<redacted>/models"},
		{"a short path part is kept", "GET /v1/models", "GET /v1/models"},
		{"the exact token", "Bearer " + scrubToken, "Bearer <redacted>"},
		{"a 12-character piece, upper case", "x 3456789ABCDE y", "x <redacted> y"},
		{"overlapping pieces merge into one", "t=TOK-0123456789AB.", "t=<redacted>."},
		{"an 11-character piece is kept", "tok-0123456", "tok-0123456"},
		{"unrelated text", "no secret here", "no secret here"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := scr.scrubParts(tc.in); got != tc.want {
				t.Errorf("scrubParts(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
	if got := (scrubber{}).scrubParts("tok " + scrubToken); got != "tok "+scrubToken {
		t.Errorf("a scrubber with no secrets changed the text: %q", got)
	}
}

// TestScrubPartsRemovesEveryPiece: every 12-character piece of every
// credential set (the base URL excepted) is removed, in either case.
func TestScrubPartsRemovesEveryPiece(t *testing.T) {
	vars := map[string]string{}
	for k, v := range credentialSentinels {
		vars[k] = v
	}
	scr := newScrubber(fakeEnv(vars, "/h"))
	for name, v := range credentialSentinels {
		if name == "ANTHROPIC_BASE_URL" {
			continue
		}
		for i := 0; i+minPartLen <= len(v); i++ {
			for _, piece := range []string{v[i : i+minPartLen], strings.ToUpper(v[i : i+minPartLen])} {
				if got := scr.scrubParts("<" + piece + ">"); got != "<"+scrubbedValue+">" {
					t.Errorf("%s piece %q: %q", name, piece, got)
				}
			}
		}
	}
	// scrub alone replaces only the exact values.
	piece := scrubToken[2:14]
	if got := gatewayScrubber().scrub(piece + " " + scrubToken); got != piece+" "+scrubbedValue {
		t.Errorf("scrub = %q", got)
	}
}

func TestScrubEvidence(t *testing.T) {
	scr := gatewayScrubber()
	for _, word := range []string{"auth", "Authorization", "TOKEN", "Bearer", "api key", "api_key", "API-Key", "apikey", "Cookie", "secret", "PASSWORD"} {
		in := "kept line\n" + "x " + word + " y\n"
		if got, want := scr.scrubEvidence(in), "kept line\n"+withheldLine+"\n"; got != want {
			t.Errorf("%q: %q, want %q", word, got, want)
		}
	}
	in := "❯ \nreach " + "gateway.example.invalid" + " via 3456789abcde\n"
	if got, want := scr.scrubEvidence(in), "❯ \nreach <redacted> via <redacted>\n"; got != want {
		t.Errorf("parts: %q, want %q", got, want)
	}
	if got := (scrubber{}).scrubEvidence("a\ntokens=3\n"); got != "a\n"+withheldLine+"\n" {
		t.Errorf("no secrets: %q", got)
	}
}
