// err_description_prefix.go provides the prefix-match helper used by
// TestEnvelopeDiff_Error to compare err_description values across the CLI and
// Client envelopes.
//
// Policy (HI-6 resolution):
//
// err_name is compared for full equality. err_description is compared by
// prefix: the substring up to and including the first ':' character. This
// sidesteps OS-specific wrapped-error wording drift (e.g. "tmux session
// create: no such file" on Linux vs macOS) without per-err_name selectors in
// the nondeterministic.json manifest schema.
//
// When neither description contains a ':', full-string equality is required.
// When one description has a ':' but the other does not, the `:` side's
// prefix is compared against the full non-`:` side — effectively a prefix
// match that tolerates absence of an OS tail.
package envelope_diff

import "strings"

// errDescriptionPrefix returns the prefix of s up to and including the first
// ':' character. If s contains no ':', it returns s unchanged so the caller
// can apply full-string equality semantics.
//
//	"tmux session create: no such file" → "tmux session create:"
//	"spawn id-1 state ended is not interactive" → "spawn id-1 state ended is not interactive"
//	"" → ""
func errDescriptionPrefix(s string) string {
	if i := strings.Index(s, ":"); i >= 0 {
		return s[:i+1]
	}
	return s
}

// errDescriptionsMatch reports whether the CLI and Client err_descriptions are
// considered equivalent under the prefix-match policy:
//
//   - If either description contains a ':', compare the prefixes of both
//     (prefix of a no-colon description is the whole string, so this handles
//     the asymmetric case gracefully).
//   - If neither description contains a ':', require full equality.
func errDescriptionsMatch(cli, client string) bool {
	cliHasColon := strings.Contains(cli, ":")
	clientHasColon := strings.Contains(client, ":")

	if !cliHasColon && !clientHasColon {
		// Both are OS-tail-free; require full equality.
		return cli == client
	}
	// At least one has a colon: compare prefixes.
	return errDescriptionPrefix(cli) == errDescriptionPrefix(client)
}
