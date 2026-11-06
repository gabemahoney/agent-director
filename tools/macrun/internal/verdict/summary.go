package verdict

import (
	"fmt"
	"strings"

	"github.com/gabemahoney/agent-director/tools/macrun/internal/model"
)

// MaxSummaryBytes caps the Markdown appended to a step's summary, below
// GitHub's 1 MiB limit on one step's summary.
const MaxSummaryBytes = 900 << 10

// shortenedRows are the row limits tried, in turn, to fit the summary under
// its cap.
var shortenedRows = []int{1000, 100, 10, 0}

// Markdown returns the verdict as Markdown: the tested commit, the verdict,
// every reason, the failed cases, the Expected Failures, the unexpected
// passes, the unmatched marks, the coverage, and each part's completeness,
// links and versions. Case results stay in the verdict's JSON. With limit
// above 0, long lists are cut until the Markdown fits in limit bytes, with a
// note that the full lists are in mac-run-<attempt>.
func Markdown(v model.AttemptVerdict, limit int) string {
	full := render(v, -1)
	if limit <= 0 || len(full) <= limit {
		return full
	}
	for _, rows := range shortenedRows {
		if short := render(v, rows); len(short) <= limit {
			return short
		}
	}
	return truncate(render(v, 0), limit, v.Attempt)
}

// render renders v, each long list cut to rows rows; rows below 0 keeps
// every row.
func render(v model.AttemptVerdict, rows int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Mac Run verdict: %s\n\n", v.Verdict)
	fmt.Fprintf(&b, "- Tested commit: `%s`\n", v.Commit)
	fmt.Fprintf(&b, "- Run: [run %d, attempt %d](%s)\n", v.RunID, v.Attempt, v.RunURL)
	fmt.Fprintf(&b, "- Coverage: Go %s, testplan %s\n", v.Coverage.Go, v.Coverage.Testplan)
	if rows >= 0 {
		fmt.Fprintf(&b, "\n%s\n", shortenedNote(v.Attempt))
	}

	t := table{rows: rows, attempt: v.Attempt}
	for _, r := range v.Reasons {
		t.add(cell(string(r.Kind)), cell(r.Part), cell(string(r.Case)), cell(r.Detail))
	}
	t.write(&b, "Incompleteness reasons", "Reason", "Part", "Case", "Detail")
	for _, f := range v.FailedCases {
		marked := "no"
		if f.Marked {
			marked = "yes"
		}
		t.add(cell(string(f.Case)), cell(f.Part), cell(string(f.Reason)), marked)
	}
	t.write(&b, "Failed cases", "Case", "Part", "Reason", "Expected Failure")
	for _, e := range v.ExpectedFailures {
		t.add(cell(string(e.Case)), cell(e.Part), cell(resultText(e.Result, e.Reason)), cell(e.Bug), cell(e.Note))
	}
	t.write(&b, "Expected Failures", "Case", "Part", "Result", "Bug", "Note")
	for _, e := range v.UnexpectedPasses {
		t.add(cell(string(e.Case)), cell(e.Part), cell(e.Bug), cell(e.Note))
	}
	t.write(&b, "Unexpected passes", "Case", "Part", "Bug", "Note")
	for _, m := range v.UnmatchedMarks {
		t.add(cell(string(m.Case)), cell(m.Bug), cell(m.Note))
	}
	t.write(&b, "Unmatched marks", "Case", "Bug", "Note")

	parts := table{rows: -1}
	for _, p := range v.Parts {
		complete := "no"
		if p.Complete {
			complete = "yes"
		}
		carried := ""
		if p.CarriedOver {
			carried = " (carried over)"
		}
		parts.add(cell(p.Name), complete, fmt.Sprintf("%d%s", p.Attempt, carried), string(p.Source), link("log", p.LogURL), link("artifact", p.ArtifactURL))
	}
	parts.write(&b, "Parts", "Part", "Complete", "Attempt", "Read from", "Log", "Artifact")
	writeVersions(&b, v.Parts)
	return b.String()
}

// shortenedNote says where the cut lists are whole.
func shortenedNote(attempt int) string {
	return fmt.Sprintf("Long lists are cut to fit GitHub's summary limit; the full lists are in the artifact mac-run-%d (%s and %s).",
		attempt, MarkdownFile, JSONFile)
}

// truncate cuts md at a line end to fit limit with the note, for a verdict
// whose parts alone pass the limit.
func truncate(md string, limit, attempt int) string {
	note := "\n" + shortenedNote(attempt) + "\n"
	keep := limit - len(note)
	if keep < 0 {
		keep = 0
	}
	if keep < len(md) {
		md = md[:keep]
		if i := strings.LastIndexByte(md, '\n'); i >= 0 {
			md = md[:i+1]
		}
	}
	return md + note
}

// writeVersions writes each part's versions, one column per version name in
// the order the parts recorded them.
func writeVersions(b *strings.Builder, parts []model.PartVerdict) {
	var names []string
	seen := map[string]bool{}
	for _, p := range parts {
		for _, v := range p.Versions {
			if !seen[v.Name] {
				seen[v.Name] = true
				names = append(names, v.Name)
			}
		}
	}
	if len(names) == 0 {
		return
	}
	t := table{rows: -1}
	for _, p := range parts {
		values := map[string]string{}
		for _, v := range p.Versions {
			values[v.Name] = v.Value
		}
		row := []string{cell(p.Name)}
		for _, name := range names {
			row = append(row, cell(values[name]))
		}
		t.add(row...)
	}
	t.write(b, "Versions", append([]string{"Part"}, names...)...)
}

// table collects one Markdown table's rows.
type table struct {
	// rows caps the rows written; below 0 writes them all.
	rows    int
	attempt int
	body    [][]string
}

func (t *table) add(cells ...string) {
	t.body = append(t.body, cells)
}

// write writes the table under a heading with its row count, cut to t.rows,
// and empties it. A table with no rows is left out.
func (t *table) write(b *strings.Builder, heading string, columns ...string) {
	body := t.body
	t.body = nil
	if len(body) == 0 {
		return
	}
	fmt.Fprintf(b, "\n### %s (%d)\n\n", heading, len(body))
	fmt.Fprintf(b, "| %s |\n|%s\n", strings.Join(columns, " | "), strings.Repeat("---|", len(columns)))
	shown := body
	if t.rows >= 0 && len(body) > t.rows {
		shown = body[:t.rows]
	}
	for _, row := range shown {
		fmt.Fprintf(b, "| %s |\n", strings.Join(row, " | "))
	}
	if cut := len(body) - len(shown); cut > 0 {
		fmt.Fprintf(b, "\n_%d more in mac-run-%d._\n", cut, t.attempt)
	}
}

// resultText is a result with its reason.
func resultText(r model.Result, reason model.Reason) string {
	if reason == "" {
		return string(r)
	}
	return fmt.Sprintf("%s (%s)", r, reason)
}

// link is a Markdown link, or empty for no URL.
func link(label, url string) string {
	if url == "" {
		return ""
	}
	return fmt.Sprintf("[%s](%s)", label, url)
}

// cellEscapes escapes text for a Markdown table cell: the punctuation that
// would end the cell, start a code span, emphasis, a link or HTML, and the
// line breaks that would end the row.
var cellEscapes = strings.NewReplacer(
	`\`, `\\`, "`", "\\`", "|", `\|`, "*", `\*`, "_", `\_`, "[", `\[`, "]", `\]`,
	"<", `\<`, ">", `\>`, "~", `\~`, "&", `\&`,
	"\r\n", "<br>", "\n", "<br>", "\r", "<br>",
)

// cell escapes text for a Markdown table cell.
func cell(text string) string {
	return cellEscapes.Replace(text)
}
