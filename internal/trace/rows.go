package trace

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/0xGurg/himay/internal/model"
)

type Kind uint8

const (
	KUser Kind = iota
	KStep
	KText
	KReasoning
	KReasoningUnavailable
	KTool
	KSubagent
	KFileChange
	KShell
	KSkill
	KInstructionLoaded
	KInstructionUpdated
	KToolsChanged
	KSystem
	KSynthetic
	KModelSwitched
	KAgentSwitched
	KLocationSwitched
	KRetry
	KCompaction
	KIdle
	KError
	KUnknown
)

var kindNames = [...]string{
	"user", "step", "text", "reasoning", "reasoning-n/a", "tool", "subagent", "file",
	"shell", "skill", "instruction", "instr-update", "tools-changed", "system", "synthetic",
	"model-switch", "agent-switch", "loc-switch", "retry", "compaction", "idle", "error", "unknown",
}

func (k Kind) String() string { return kindNames[k] }

// Status is a row outcome marker.
type Status uint8

const (
	SNone Status = iota
	SOK
	SFail
	SRunning
)

// Row is one line of the trace.
type Row struct {
	Kind     Kind
	Depth    int
	MsgID    string
	Item     int // content index, or -1 for message-level rows
	Seq      int64
	Time     time.Time
	Dur      time.Duration
	Agent    string
	Model    model.ModelRef
	Label    string
	Status   Status
	Evidence model.Evidence
	Child    string   // subagent session ID
	Files    []string // file paths for file-change/patch rows
	HasKids  bool     // row has deeper rows directly below it
}

// Searchable returns the text used for in-session search and filtering.
func (r Row) Searchable() string {
	return strings.ToLower(r.Kind.String() + " " + r.Agent + " " + r.Model.String() + " " + r.Label + " " + strings.Join(r.Files, " "))
}

// Build converts messages (in seq order) into rows. It is pure and cheap, so
// callers rebuild the whole trace whenever messages change.
func Build(msgs []model.Message) []Row {
	var rows []Row
	prevAgent := ""
	for i, m := range msgs {
		base := Row{MsgID: m.ID, Item: -1, Seq: m.Seq, Time: m.Created, Evidence: model.Recorded}
		if m.Malformed {
			r := base
			r.Kind, r.Status = KUnknown, SFail
			r.Label = fmt.Sprintf("malformed or partially written %q message: %s", m.Type, oneLine(m.RawPreview, 120))
			rows = append(rows, r)
			continue
		}
		switch m.Type {
		case "user":
			r := base
			r.Kind = KUser
			r.Label = quote(m.Text, m.TextLen)
			var extra []string
			for _, a := range m.Agents {
				extra = append(extra, "@"+a)
			}
			for _, s := range m.Skills {
				extra = append(extra, "skill:"+s)
			}
			for _, f := range m.Files {
				extra = append(extra, "📎"+fileLabel(f))
			}
			if len(extra) > 0 {
				r.Label += "  " + strings.Join(extra, " ")
			}
			rows = append(rows, r)
		case "assistant":
			if prevAgent != "" && m.Agent != "" && m.Agent != prevAgent {
				r := base
				r.Kind, r.Evidence = KAgentSwitched, model.Derived
				r.Label = prevAgent + " → " + m.Agent
				r.Agent = m.Agent
				rows = append(rows, r)
			}
			if m.Agent != "" {
				prevAgent = m.Agent
			}
			rows = append(rows, stepRows(m, base)...)
			if r, ok := inferredRetry(msgs, i); ok {
				rows = append(rows, r)
			}
		case "agent-switched":
			r := base
			r.Kind = KAgentSwitched
			to := m.Agent
			r.Label = prevString(m.Previous, "agent") + " → " + orDash(to)
			if to != "" {
				prevAgent = to
			}
			rows = append(rows, r)
		case "model-switched":
			r := base
			r.Kind = KModelSwitched
			r.Model = m.Model
			r.Label = prevModel(m.Previous) + " → " + m.Model.String()
			rows = append(rows, r)
		case "location-switched":
			r := base
			r.Kind = KLocationSwitched
			r.Label = prevString(m.Previous, "location") + " → " + orDash(m.Text)
			rows = append(rows, r)
		case "system":
			rows = append(rows, systemRow(m, base))
		case "synthetic":
			rows = append(rows, syntheticRows(m, base)...)
		case "skill":
			r := base
			r.Kind = KSkill
			r.Label = "skill activated: " + orDash(m.Description)
			rows = append(rows, r)
		case "shell":
			r := base
			r.Kind = KShell
			r.Label = "$ " + m.Text
			switch m.Status {
			case "exited":
				r.Status = SOK
			case "running":
				r.Status = SRunning
			case "timeout", "killed":
				r.Status = SFail
				r.Label += "  (" + m.Status + ")"
			}
			rows = append(rows, r)
		case "compaction":
			r := base
			r.Kind = KCompaction
			r.Label = strings.TrimSpace("compaction " + m.Reason + " · " + orDash(m.Status))
			if m.TextLen > 0 {
				r.Label += fmt.Sprintf(" · summary %s", size(m.TextLen))
			}
			if m.Status == "failed" {
				r.Status = SFail
			}
			if m.Err != nil {
				r.Label += " · " + m.Err.Message
			}
			rows = append(rows, r)
		case "idle":
			r := base
			r.Kind = KIdle
			r.Label = "idle · " + orDash(m.Outcome)
			switch m.Outcome {
			case "succeeded":
				r.Status = SOK
			case "failed", "interrupted":
				r.Status = SFail
			}
			rows = append(rows, r)
		default:
			r := base
			r.Kind = KUnknown
			keys := make([]string, 0, len(m.Extra))
			for k := range m.Extra {
				keys = append(keys, k)
			}
			sort.Strings(keys) // map order is random; keep labels stable
			r.Label = fmt.Sprintf("unrecognized message type %q  keys=%v", m.Type, keys)
			rows = append(rows, r)
		}
	}
	for i := range rows {
		if i+1 < len(rows) && rows[i+1].Depth > rows[i].Depth {
			rows[i].HasKids = true
		}
	}
	return rows
}

// stepRows renders one assistant message (one LLM step) and its content items.
func stepRows(m model.Message, base Row) []Row {
	h := base
	h.Kind = KStep
	h.Agent, h.Model = m.Agent, m.Model
	h.Time = m.Time.Created
	if !m.Time.Completed.IsZero() && !m.Time.Created.IsZero() {
		h.Dur = m.Time.Completed.Sub(m.Time.Created)
	}
	var parts []string
	if m.Finish != "" {
		parts = append(parts, "finish="+m.Finish)
	}
	if m.Tokens.Known {
		parts = append(parts, fmt.Sprintf("%s tok", count(m.Tokens.Total())))
	}
	if m.Cost != nil && *m.Cost > 0 {
		parts = append(parts, fmt.Sprintf("$%.4f", *m.Cost))
	}
	if len(m.Items) == 0 && m.Err == nil {
		parts = append(parts, "no content")
	}
	h.Label = strings.Join(parts, " · ")
	switch {
	case m.Err != nil:
		h.Status = SFail
	case m.Time.Completed.IsZero() && m.Finish == "":
		h.Status = SRunning
	}
	rows := []Row{h}

	if m.Retry != nil {
		r := base
		r.Kind, r.Depth, r.Status = KRetry, 1, SFail
		r.Time = m.Retry.At
		r.Label = fmt.Sprintf("retry attempt %d · %s", m.Retry.Attempt, oneLine(m.Retry.Error, 120))
		rows = append(rows, r)
	}
	for _, it := range m.Items {
		rows = append(rows, itemRows(m, base, it)...)
	}
	if m.Err != nil {
		r := base
		r.Kind, r.Depth, r.Status = KError, 1, SFail
		r.Label = m.Err.Type + ": " + oneLine(m.Err.Message, 160)
		rows = append(rows, r)
	}
	return rows
}

func itemRows(m model.Message, base Row, it model.ItemSummary) []Row {
	r := base
	r.Item, r.Depth = it.Index, 1
	r.Agent, r.Model = m.Agent, m.Model
	r.Time = it.Start // zero when OpenCode recorded no item time; rendered blank

	switch it.Kind {
	case "text":
		r.Kind = KText
		r.Label = quote(it.TextPreview, it.TextLen)
		return []Row{r}
	case "reasoning":
		if it.TextLen == 0 {
			r.Kind = KReasoningUnavailable
			if it.Encrypted {
				r.Label = "reasoning encrypted by provider (not readable)"
			} else {
				r.Label = "reasoning recorded with no text"
			}
			return []Row{r}
		}
		r.Kind = KReasoning
		r.Label = fmt.Sprintf("(%s) %s", size(it.TextLen), oneLine(it.TextPreview, 160))
		return []Row{r}
	case "tool":
		return toolRows(r, it)
	default:
		r.Kind = KUnknown
		r.Label = fmt.Sprintf("unrecognized content item %q", it.Kind)
		return []Row{r}
	}
}

func toolRows(r Row, it model.ItemSummary) []Row {
	r.Kind = KTool
	if !it.Start.IsZero() && !it.End.IsZero() {
		r.Dur = it.End.Sub(it.Start)
	}
	switch it.Status {
	case "completed":
		r.Status = SOK
	case "error":
		r.Status = SFail
	case "running", "streaming", "pending":
		r.Status = SRunning
	}
	r.Label = it.ToolName
	if it.InputPreview != "" {
		r.Label += "  " + oneLine(it.InputPreview, 140)
	}
	if it.Err != nil {
		r.Label += "  ✗ " + oneLine(it.Err.Message, 100)
	}
	isSubagent := it.ToolName == "task" || it.ToolName == "subagent"
	if isSubagent {
		r.Kind = KSubagent
		r.Child = it.ChildSession
		r.Model = it.ChildModel
		r.Agent = it.ChildAgent
		r.Label = "→ " + orDash(it.ChildAgent) + "  " + oneLine(it.InputPreview, 120)
		if it.Err != nil {
			r.Label += "  ✗ " + oneLine(it.Err.Message, 100)
		}
	}
	if it.ToolName == "skill" {
		r.Kind = KSkill
		r.Label = "skill loaded: " + orDash(it.InputPreview)
	}
	rows := []Row{r}
	for _, p := range it.Loaded {
		l := r
		l.Kind, l.Depth, l.Status, l.Dur = KInstructionLoaded, r.Depth+1, SNone, 0
		l.Label = "instructions auto-loaded: " + p
		l.Files = []string{p}
		rows = append(rows, l)
	}
	if len(it.Files) > 0 && it.Status == "completed" {
		f := r
		f.Kind, f.Depth, f.Status, f.Dur = KFileChange, r.Depth+1, SNone, 0
		f.Files = it.Files
		names := make([]string, len(it.Files))
		for i, p := range it.Files {
			names[i] = filepath.Base(p)
		}
		f.Label = "changed " + strings.Join(names, ", ")
		rows = append(rows, f)
	}
	return rows
}

// inferredRetry flags an errored step that is immediately followed by another
// step from the same agent with no user message between them.
func inferredRetry(msgs []model.Message, i int) (Row, bool) {
	m := msgs[i]
	if m.Err == nil || m.Retry != nil || m.Err.Type == "aborted" {
		return Row{}, false
	}
	for j := i + 1; j < len(msgs); j++ {
		n := msgs[j]
		switch n.Type {
		case "assistant":
			if n.Agent != m.Agent || n.Retry != nil {
				return Row{}, false
			}
			return Row{Kind: KRetry, Depth: 1, MsgID: n.ID, Item: -1, Seq: n.Seq, Time: n.Created,
				Agent: n.Agent, Model: n.Model, Evidence: model.Derived,
				Label: "next step after error looks like a retry (inferred)"}, true
		case "user", "idle":
			return Row{}, false
		}
	}
	return Row{}, false
}

func systemRow(m model.Message, base Row) Row {
	r := base
	var md struct {
		Notice  string   `json:"notice"`
		Sources []string `json:"instructionSources"`
	}
	_ = json.Unmarshal(m.Metadata, &md)
	text := oneLine(m.Text, 160)
	switch {
	case strings.HasPrefix(m.Text, "The available tools have changed"):
		r.Kind = KToolsChanged
		r.Label = strings.TrimPrefix(text, "The available tools have changed. ")
	case md.Notice == "instructions" || strings.HasPrefix(m.Text, "Instructions"):
		r.Kind = KInstructionUpdated
		r.Label = text
		if len(md.Sources) > 0 {
			r.Label = "instructions updated: " + strings.Join(md.Sources, ", ")
		}
	default:
		r.Kind = KSystem
		r.Label = text
	}
	if m.Description != "" {
		r.Label = m.Description + " · " + r.Label
	}
	return r
}

func syntheticRows(m model.Message, base Row) []Row {
	r := base
	r.Kind = KSynthetic
	var md struct {
		Instruction struct {
			Paths []string `json:"paths"`
		} `json:"instruction"`
	}
	_ = json.Unmarshal(m.Metadata, &md)
	if len(md.Instruction.Paths) > 0 || strings.HasPrefix(m.Text, "Instructions from:") {
		r.Kind = KInstructionLoaded
		r.Files = md.Instruction.Paths
		r.Label = "instructions loaded: " + strings.Join(md.Instruction.Paths, ", ")
		if len(md.Instruction.Paths) == 0 {
			r.Label = oneLine(m.Text, 160)
		}
		return []Row{r}
	}
	label := m.Description
	if label == "" {
		label = oneLine(m.Text, 160)
	}
	r.Label = label
	return []Row{r}
}

func prevModel(b json.RawMessage) string {
	var m model.ModelRef
	if json.Unmarshal(b, &m) != nil {
		return "—"
	}
	return m.String()
}

func prevString(b json.RawMessage, key string) string {
	if len(b) > 0 && b[0] != '{' && b[0] != '"' {
		return string(b) // json_extract already unquoted a string value
	}
	var s string
	if json.Unmarshal(b, &s) == nil && s != "" {
		return s
	}
	var obj map[string]any
	if json.Unmarshal(b, &obj) == nil {
		if v, ok := obj[key].(string); ok {
			return v
		}
	}
	return "—"
}

func fileLabel(f model.FileRef) string {
	n := f.Name
	if n == "" {
		n = f.Mime
	}
	if f.Size > 0 {
		n += " (" + size(f.Size*3/4) + ")" // base64 → bytes
	}
	return n
}

func quote(s string, full int) string {
	s = oneLine(s, 160)
	if full > len(s) {
		return fmt.Sprintf("%q… (%s)", s, size(full))
	}
	return fmt.Sprintf("%q", s)
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func size(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func count(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}

// Count and Size are exported formatting helpers shared with the TUI.
func Count(n int64) string { return count(n) }
func Size(n int) string    { return size(n) }
