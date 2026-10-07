package api

import (
	"time"

	"github.com/gabemahoney/agent-director/internal/config"
)

// OlderThanForm is the form of expire's window override, older_than, that
// ParseOlderThan accepts, in the words every surface uses: the CLI's and MCP's
// refusals of a value ParseOlderThan rejects state it, and so does MCP's
// tools/list schema for the param. Its day limit is
// config.MaxExpireRetentionDays (106751).
const OlderThanForm = `a non-negative Go duration like "12h" or trailing-d days like "7d" up to "106751d"`

// ParseOlderThan parses expire's window override, older_than, the one parser
// the CLI and MCP surfaces share (b.hxn). It accepts Go's time.ParseDuration
// form, or decimal digits followed by d for a number of days ("7d"), which
// Go's parser lacks and the retention setting (defaults.expire_retention_days)
// counts in.
//
// ok is false for a value in neither form, for a Go duration below zero, such
// as "-2h", and for a day count above 106751 (config.MaxExpireRetentionDays,
// the largest whole number of days a time.Duration holds), such as "365000d"
// (b.sgw): Expire selects every finished row for a zero window, so a day
// count that wrapped the window could delete the whole finished history
// (Expire refuses a negative window with ErrInvalidFlags, b.f4v). The day
// count is checked digit by digit, so no count, however long, wraps. A caller
// that means every finished row passes "0d" or "0s". Each surface refuses a
// rejected value with ErrInvalidFlags stating OlderThanForm.
func ParseOlderThan(s string) (d time.Duration, ok bool) {
	if n := len(s); n > 1 && s[n-1] == 'd' {
		var days int
		for _, c := range s[:n-1] {
			if c < '0' || c > '9' {
				return 0, false
			}
			days = days*10 + int(c-'0')
			if days > config.MaxExpireRetentionDays {
				return 0, false
			}
		}
		return time.Duration(days) * 24 * time.Hour, true
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, false
	}
	return d, true
}
