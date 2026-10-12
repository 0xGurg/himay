// Package view holds presentation logic shared by the TUI and the web UI:
// row detail loading, filter groups, session stats, and the provenance run.
// It knows nothing about terminals or HTML.
package view

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/0xGurg/himay/internal/decode"
	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/provenance"
	"github.com/0xGurg/himay/internal/store"
	"github.com/0xGurg/himay/internal/trace"
)

// DetailCap is the number of bytes shown before an explicit expand.
const DetailCap = 64 << 10

// Section is one titled block of row detail.
type Section struct {
	Title string
	Body  string
	Diff  bool
	KV    [][2]string // key/value pairs instead of Body
}

// FindMsg returns the message with the given ID, or nil.
func FindMsg(msgs []model.Message, id string) *model.Message {
	for i := range msgs {
		if msgs[i].ID == id {
			return &msgs[i]
		}
	}
	return nil
}

// NeedsLoad reports whether a row's detail requires a database read.
func NeedsLoad(r trace.Row, m *model.Message) bool {
	if m == nil {
		return false
	}
	if m.Malformed {
		return true
	}
	if r.Item >= 0 {
		return true
	}
	switch r.Kind {
	case trace.KUser, trace.KSystem, trace.KSynthetic, trace.KToolsChanged, trace.KInstructionLoaded,
		trace.KInstructionUpdated, trace.KCompaction, trace.KSkill, trace.KShell, trace.KUnknown,
		trace.KLocationSwitched:
		return true
	}
	return false
}

// Header describes a row from already-loaded data (no I/O).
func Header(r trace.Row, m *model.Message) Section {
	var kv [][2]string
	add := func(k, v string) {
		if v != "" {
			kv = append(kv, [2]string{k, v})
		}
	}
	add("kind", r.Kind.String())
	if m != nil {
		add("message", fmt.Sprintf("%s · seq %d · %s", m.ID, m.Seq, m.Type))
		if !m.Created.IsZero() {
			add("time", m.Created.Format("2006-01-02 15:04:05.000"))
		}
		if m.Type == "assistant" {
			add("agent", orDash(m.Agent))
			add("model", m.Model.String())
			add("finish", orDash(m.Finish))
			if m.Tokens.Known {
				t := m.Tokens
				add("tokens", fmt.Sprintf("in %d · out %d · reasoning %d · cache r/w %d/%d", t.Input, t.Output, t.Reasoning, t.CacheRead, t.CacheWrite))
			} else {
				add("tokens", "— (not recorded)")
			}
			if m.Cost != nil {
				add("cost", fmt.Sprintf("$%.6f (as reported by OpenCode)", *m.Cost))
			} else {
				add("cost", "— (not recorded)")
			}
			if !m.Time.Completed.IsZero() && !m.Time.Created.IsZero() {
				add("duration", trace.Duration(m.Time.Completed.Sub(m.Time.Created)))
			}
			if m.Err != nil {
				add("error", m.Err.Type+": "+m.Err.Message)
			}
			if m.Retry != nil {
				add("retry", fmt.Sprintf("attempt %d: %s", m.Retry.Attempt, m.Retry.Error))
			}
		}
		add("desc", m.Description)
		add("outcome", m.Outcome)
		if m.Type != "assistant" {
			add("status", m.Status)
		}
		for _, f := range m.Files {
			add("file", fmt.Sprintf("%s %s (%s, bytes not loaded)", f.Name, f.Mime, trace.Size(f.Size*3/4)))
		}
		if m.Malformed {
			add("warning", "message JSON is malformed or partially written")
		}
		if r.Item >= 0 {
			for _, it := range m.Items {
				if it.Index != r.Item {
					continue
				}
				if it.ToolName != "" {
					add("tool", it.ToolName+" · "+it.ToolCallID)
					add("status", it.Status)
				}
				if !it.Start.IsZero() && !it.End.IsZero() {
					add("ran", trace.Duration(it.End.Sub(it.Start)))
				}
				if it.ChildSession != "" {
					add("subagent", it.ChildSession)
					add("child", orDash(it.ChildAgent)+" · "+it.ChildModel.String())
				}
			}
		}
	}
	if len(r.Files) > 0 {
		add("files", strings.Join(r.Files, "\n"))
	}
	if r.Evidence != model.Recorded {
		add("evidence", r.Evidence.String()+" — inferred by Himay, not recorded by OpenCode")
	}
	return Section{Title: "row", KV: kv}
}

// LoadDetail reads the full content behind a row. full disables the size cap.
func LoadDetail(ctx context.Context, st *store.Store, r trace.Row, m *model.Message, full bool) ([]Section, bool, error) {
	secs := []Section{Header(r, m)}
	truncated := false
	capText := func(s string) string {
		s = cleanText(s)
		if full || len(s) <= DetailCap {
			return s
		}
		truncated = true
		return s[:DetailCap] + fmt.Sprintf("\n\n… truncated: showing %s of %s", trace.Size(DetailCap), trace.Size(len(s)))
	}
	if m != nil && m.Malformed && m.RawPreview != "" {
		secs = append(secs, Section{Title: "raw preview", Body: m.RawPreview})
	}
	if !NeedsLoad(r, m) {
		return secs, false, nil
	}
	if r.Item >= 0 {
		js, err := st.ItemJSON(ctx, r.MsgID, r.Item)
		if err != nil {
			return nil, false, err
		}
		switch r.Kind {
		case trace.KText, trace.KReasoning, trace.KReasoningUnavailable:
			text, enc := decode.ItemText(js)
			if enc && text == "" {
				text = "The provider returned encrypted reasoning. OpenCode stored only the ciphertext; Himay does not and cannot decrypt it."
			}
			title := "text"
			if r.Kind != trace.KText {
				title = "saved reasoning (as stored by OpenCode)"
			}
			secs = append(secs, Section{Title: title, Body: capText(text)})
		default:
			td := decode.ToolDetail(js)
			if len(td.Input) > 0 {
				secs = append(secs, Section{Title: "input", Body: capText(PrettyJSON(td.Input))})
			}
			if td.Err != nil {
				secs = append(secs, Section{Title: "error", Body: td.Err.Type + ": " + td.Err.Message})
			}
			if r.Kind == trace.KInstructionLoaded && len(r.Files) > 0 {
				if sec := provenance.Section(td.Output, r.Files[0]); sec != "" {
					secs = append(secs, Section{Title: "instructions from " + r.Files[0] + " [recorded]", Body: capText(sec)})
				}
			}
			if td.Diff != "" {
				secs = append(secs, Section{Title: "diff", Body: capText(td.Diff), Diff: true})
			}
			if td.Output != "" {
				t := "output"
				if td.Truncated {
					t += " (OpenCode truncated this output when storing it)"
				}
				secs = append(secs, Section{Title: t, Body: capText(td.Output)})
			}
			if len(td.Metadata) > 0 && td.Diff == "" {
				secs = append(secs, Section{Title: "metadata", Body: capText(PrettyJSON(trimMetadata(td.Metadata)))})
			}
		}
		return secs, truncated, nil
	}
	var body string
	var err error
	if r.Kind == trace.KUnknown || r.Kind == trace.KLocationSwitched {
		body, err = st.MessageJSON(ctx, r.MsgID)
		body = PrettyJSON(json.RawMessage(body))
	} else {
		body, err = st.MessageText(ctx, r.MsgID)
	}
	if err != nil {
		return nil, false, err
	}
	title := "content"
	switch r.Kind {
	case trace.KCompaction:
		title = "compaction summary"
	case trace.KUnknown:
		title = "raw JSON (inline file bytes omitted)"
	case trace.KUser:
		title = "user message"
	}
	secs = append(secs, Section{Title: title, Body: capText(body)})
	return secs, truncated, nil
}

// cleanText strips ANSI escape sequences and control characters (other than
// newline and tab) from untrusted content such as tool output.
func cleanText(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

func PrettyJSON(b json.RawMessage) string {
	var buf bytes.Buffer
	if json.Indent(&buf, b, "", "  ") != nil {
		return string(b)
	}
	return buf.String()
}

// trimMetadata drops bulky metadata fields that duplicate output.
func trimMetadata(b json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) != nil {
		return b
	}
	for _, k := range []string{"preview", "output", "filediff", "diagnostics"} {
		if v, ok := m[k]; ok && len(v) > 200 {
			m[k] = json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("<%s omitted>", trace.Size(len(v)))))
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return b
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}
