package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// BusySendLaunchCapabilities contains only a Launch's immutable admission
// facts. Force and transcript evidence belong to the executing route, not to
// this record. Modes are in the permanent public order: Wait Turn, Steer Turn.
type BusySendLaunchCapabilities struct {
	HeldCount int64
	HeldBytes int64
	Modes     []BusySendModeLimits
}

func (c BusySendLaunchCapabilities) Mode(mode BusySendMode) (BusySendModeLimits, bool) {
	for _, m := range c.Modes {
		if m.Mode == mode {
			return m, true
		}
	}
	return BusySendModeLimits{}, false
}

var errBusySendCapabilities = errors.New("store: invalid busy-send Launch capabilities")

// newBusySendLaunchCapabilities must find an actual authoring row. A retained
// sender need not know the provider, but only a covering version creates its
// Launch. Never invent capabilities to preserve a coverage flag.
func newBusySendLaunchCapabilities(provider string, coverage BusySendCoverage) (*BusySendLaunchCapabilities, error) {
	if coverage == BusySendNotCovered {
		return nil, nil
	}
	if coverage != BusySendCovered {
		return nil, errBusySendCapabilities
	}
	if provider == "" {
		provider = AgentClaude
	}
	for _, row := range busySendCapabilityTable {
		if row.Provider != provider {
			continue
		}
		c := &BusySendLaunchCapabilities{HeldCount: row.HeldCount, HeldBytes: row.HeldBytes,
			Modes: append([]BusySendModeLimits(nil), row.Modes...)}
		for i := range c.Modes {
			m := &c.Modes[i]
			m.Wrapping.Escapes = append([]BusySendEscape(nil), m.Wrapping.Escapes...)
		}
		return c, validateBusySendLaunchCapabilities(c)
	}
	return nil, fmt.Errorf("%w: no covering capability table row", errBusySendCapabilities)
}

func validateBusySendLaunchCapabilities(c *BusySendLaunchCapabilities) error {
	if c == nil || c.HeldCount <= 0 || c.HeldBytes <= 0 || len(c.Modes) < 1 || len(c.Modes) > 2 {
		return errBusySendCapabilities
	}
	seen := make(map[BusySendMode]bool)
	for _, m := range c.Modes {
		if (m.Mode != BusySendWaitTurn && m.Mode != BusySendSteerTurn) || seen[m.Mode] ||
			m.MessageBytes <= 0 || m.PromptBytes <= 0 || m.WrapperBytes < 0 || m.TextKind != BusySendTextUTF8TabLF {
			return errBusySendCapabilities
		}
		seen[m.Mode] = true
		switch m.MeasureKind {
		case BusySendMeasureWrapperBudget:
			if !m.Wrapping.zero() {
				return errBusySendCapabilities
			}
		case BusySendMeasureLiteralEscapes:
			for _, e := range m.Wrapping.Escapes {
				if !validBusySendEscape(e) {
					return errBusySendCapabilities
				}
			}
		default:
			return errBusySendCapabilities
		}
	}
	if !seen[BusySendWaitTurn] {
		return errBusySendCapabilities
	}
	return nil
}

// heldLimitArgs supplies the nullable Launch columns in the initial INSERT;
// snapshots are never installed by a later UPDATE.
func heldLimitArgs(c *BusySendLaunchCapabilities) (any, any) {
	if c == nil {
		return nil, nil
	}
	return c.HeldCount, c.HeldBytes
}

func insertBusySendLaunchCapabilities(ctx context.Context, q busySendLaunchSQL, storeID, launchID string, c *BusySendLaunchCapabilities) error {
	if c == nil {
		return nil
	}
	if err := validateBusySendLaunchCapabilities(c); err != nil {
		return err
	}
	for _, m := range c.Modes {
		_, err := q.ExecContext(ctx, `INSERT INTO busy_send_launch_modes
		    (launch_id, send_mode, store_id, message_bytes, prompt_bytes, text_kind, wrapper_bytes, measure_kind, prefix, suffix, escape_count)
		    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, launchID, string(m.Mode), storeID, m.MessageBytes, m.PromptBytes,
			string(m.TextKind), m.WrapperBytes, string(m.MeasureKind), m.Wrapping.Prefix, m.Wrapping.Suffix, len(m.Wrapping.Escapes))
		if err != nil {
			return err
		}
		for i, e := range m.Wrapping.Escapes {
			if _, err := q.ExecContext(ctx, `INSERT INTO busy_send_launch_escapes
			    (launch_id, send_mode, escape_index, store_id, raw, escaped) VALUES (?, ?, ?, ?, ?, ?)`,
				launchID, string(m.Mode), i, storeID, e.Raw, e.Escaped); err != nil {
				return err
			}
		}
	}
	return nil
}

// readBusySendLaunchCapabilities uses its caller's existing transaction and
// store identity. It does not open a connection or consult a provider table.
func readBusySendLaunchCapabilities(ctx context.Context, q coreSQL, storeID, launchID string) (*BusySendLaunchCapabilities, error) {
	return readBusySendLaunchCapabilitiesPresent(ctx, q, storeID, launchID, true, true)
}

// The presence flags are only for validate-first exempt migration: absent
// tables are empty, never permission to reconstruct an established snapshot.
func readBusySendLaunchCapabilitiesPresent(ctx context.Context, q coreSQL, storeID, launchID string, modesPresent, escapesPresent bool) (*BusySendLaunchCapabilities, error) {
	var identity, covered, count, size any
	if err := q.QueryRowContext(ctx, `SELECT store_id, busy_send, held_count_limit, held_bytes_limit
	    FROM busy_send_launches WHERE launch_id = ?`, launchID).Scan(&identity, &covered, &count, &size); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errBusySendCapabilities
		}
		return nil, err
	}
	if identity != storeID {
		return nil, errBusySendCapabilities
	}
	coverage, err := decodeBusySendCoverage(covered)
	if err != nil {
		return nil, errBusySendCapabilities
	}
	c := &BusySendLaunchCapabilities{}
	if coverage == BusySendCovered {
		var okCount, okSize bool
		c.HeldCount, okCount = count.(int64)
		c.HeldBytes, okSize = size.(int64)
		if !okCount || !okSize {
			return nil, errBusySendCapabilities
		}
	} else if count != nil || size != nil {
		return nil, errBusySendCapabilities
	}
	counts := make(map[BusySendMode]int64)
	if modesPresent {
		if err := readBusySendLaunchModes(ctx, q, storeID, launchID, c, counts); err != nil {
			return nil, err
		}
	}
	if escapesPresent {
		if err := readBusySendLaunchEscapes(ctx, q, storeID, launchID, c); err != nil {
			return nil, err
		}
	}
	for _, m := range c.Modes {
		if int64(len(m.Wrapping.Escapes)) != counts[m.Mode] {
			return nil, errBusySendCapabilities
		}
	}
	if coverage == BusySendNotCovered {
		if len(c.Modes) != 0 {
			return nil, errBusySendCapabilities
		}
		return nil, nil
	}
	return c, validateBusySendLaunchCapabilities(c)
}

func readBusySendLaunchModes(ctx context.Context, q coreSQL, storeID, launchID string, c *BusySendLaunchCapabilities, counts map[BusySendMode]int64) error {
	rows, err := q.QueryContext(ctx, `SELECT store_id, send_mode, message_bytes, prompt_bytes,
	    text_kind, wrapper_bytes, measure_kind, prefix, suffix, escape_count FROM busy_send_launch_modes
	    WHERE launch_id = ? ORDER BY CASE send_mode WHEN 'wait_turn' THEN 0 ELSE 1 END`, launchID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var identity, mode, message, prompt, kind, wrapper, measure, prefix, suffix, count any
		if err := rows.Scan(&identity, &mode, &message, &prompt, &kind, &wrapper, &measure, &prefix, &suffix, &count); err != nil {
			return err
		}
		modeText, a := mode.(string)
		messageBytes, b := message.(int64)
		promptBytes, d := prompt.(int64)
		textKind, e := kind.(string)
		wrapperBytes, f := wrapper.(int64)
		measureKind, g := measure.(string)
		prefixText, h := prefix.(string)
		suffixText, i := suffix.(string)
		escapeCount, j := count.(int64)
		if identity != storeID || !a || !b || !d || !e || !f || !g || !h || !i || !j || escapeCount < 0 || len(c.Modes) >= 2 {
			return errBusySendCapabilities
		}
		counts[BusySendMode(modeText)] = escapeCount
		c.Modes = append(c.Modes, BusySendModeLimits{Mode: BusySendMode(modeText), MessageBytes: messageBytes,
			PromptBytes: promptBytes, TextKind: BusySendTextKind(textKind), WrapperBytes: wrapperBytes,
			MeasureKind: BusySendMeasureKind(measureKind), Wrapping: BusySendWrapping{Prefix: prefixText, Suffix: suffixText}})
	}
	return rows.Err()
}

func readBusySendLaunchEscapes(ctx context.Context, q coreSQL, storeID, launchID string, c *BusySendLaunchCapabilities) error {
	rows, err := q.QueryContext(ctx, `SELECT store_id, send_mode, escape_index, raw, escaped
	    FROM busy_send_launch_escapes WHERE launch_id = ? ORDER BY send_mode, escape_index`, launchID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var identity, mode, index, raw, escaped any
		if err := rows.Scan(&identity, &mode, &index, &raw, &escaped); err != nil {
			return err
		}
		modeText, a := mode.(string)
		n, b := index.(int64)
		rawText, d := raw.(string)
		escapedText, e := escaped.(string)
		if identity != storeID || !a || !b || !d || !e {
			return errBusySendCapabilities
		}
		var m *BusySendModeLimits
		for i := range c.Modes {
			if c.Modes[i].Mode == BusySendMode(modeText) {
				m = &c.Modes[i]
			}
		}
		if m == nil || m.MeasureKind != BusySendMeasureLiteralEscapes || n != int64(len(m.Wrapping.Escapes)) {
			return errBusySendCapabilities
		}
		m.Wrapping.Escapes = append(m.Wrapping.Escapes, BusySendEscape{Raw: rawText, Escaped: escapedText})
	}
	return rows.Err()
}
