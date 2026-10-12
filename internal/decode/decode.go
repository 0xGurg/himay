// Package decode turns raw store rows into model types. Every field is decoded
// independently so one malformed or unexpected value never hides the rest.
package decode

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"

	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/store"
)

// minPlausibleMs (2000-01-01) rejects timestamps that are clearly not epoch
// milliseconds, so a bogus value never overrides a row's real time.
const minPlausibleMs = 946_684_800_000

func ms(v int64) time.Time {
	if v < minPlausibleMs {
		return time.Time{}
	}
	return time.UnixMilli(v)
}

func nullMs(v sql.NullInt64) time.Time {
	if !v.Valid {
		return time.Time{}
	}
	return ms(v.Int64)
}

func raw(s sql.NullString) json.RawMessage {
	if !s.Valid || s.String == "" || s.String == "null" {
		return nil
	}
	return json.RawMessage(s.String)
}

// ModelRef decodes {id, providerID, variant} and also the legacy/tool shape {modelID, providerID}.
func ModelRef(b json.RawMessage) model.ModelRef {
	if len(b) == 0 {
		return model.ModelRef{}
	}
	var m struct {
		ID         string `json:"id"`
		ModelID    string `json:"modelID"`
		ProviderID string `json:"providerID"`
		Variant    string `json:"variant"`
	}
	if json.Unmarshal(b, &m) != nil {
		return model.ModelRef{}
	}
	if m.ID == "" {
		m.ID = m.ModelID
	}
	return model.ModelRef{ID: m.ID, ProviderID: m.ProviderID, Variant: m.Variant}
}

// Tokens decodes {input, output, reasoning, cache{read, write}}.
func Tokens(b json.RawMessage) model.Tokens {
	if len(b) == 0 {
		return model.Tokens{}
	}
	var t struct {
		Input     *int64 `json:"input"`
		Output    *int64 `json:"output"`
		Reasoning *int64 `json:"reasoning"`
		Cache     struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	}
	if json.Unmarshal(b, &t) != nil || (t.Input == nil && t.Output == nil) {
		return model.Tokens{}
	}
	v := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	return model.Tokens{Input: v(t.Input), Output: v(t.Output), Reasoning: v(t.Reasoning),
		CacheRead: t.Cache.Read, CacheWrite: t.Cache.Write, Known: true}
}

// StepError decodes {type, message} and the legacy {name, data{message}} shape.
func StepError(b json.RawMessage) *model.StepError {
	if len(b) == 0 {
		return nil
	}
	var e struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Message string `json:"message"`
		Data    struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	if json.Unmarshal(b, &e) != nil {
		// Not an object: keep the raw text so nothing is lost.
		return &model.StepError{Type: "unknown", Message: string(b)}
	}
	if e.Type == "" {
		e.Type = e.Name
	}
	if e.Message == "" {
		e.Message = e.Data.Message
	}
	if e.Type == "" && e.Message == "" {
		return nil
	}
	return &model.StepError{Type: e.Type, Message: e.Message}
}

func retry(b json.RawMessage) *model.RetryInfo {
	if len(b) == 0 {
		return nil
	}
	var r struct {
		Attempt int64           `json:"attempt"`
		At      int64           `json:"at"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	ri := &model.RetryInfo{Attempt: r.Attempt, At: ms(r.At)}
	if e := StepError(r.Error); e != nil {
		ri.Error = e.Message
	} else if len(r.Error) > 0 {
		ri.Error = string(r.Error)
	}
	return ri
}

func stepTime(b json.RawMessage) model.StepTime {
	var t struct {
		Created   int64 `json:"created"`
		Streamed  int64 `json:"streamed"`
		Completed int64 `json:"completed"`
		// legacy/tool shapes
		Start int64 `json:"start"`
		End   int64 `json:"end"`
		Ran   int64 `json:"ran"`
	}
	if len(b) == 0 || json.Unmarshal(b, &t) != nil {
		return model.StepTime{}
	}
	if t.Created == 0 {
		t.Created = t.Start
	}
	if t.Completed == 0 {
		t.Completed = t.End
	}
	return model.StepTime{Created: ms(t.Created), Streamed: ms(t.Streamed), Completed: ms(t.Completed)}
}

func strList(s sql.NullString) []string {
	if !s.Valid {
		return nil
	}
	var in []*string
	if json.Unmarshal([]byte(s.String), &in) != nil {
		return nil
	}
	var out []string
	for _, v := range in {
		if v != nil && *v != "" {
			out = append(out, *v)
		}
	}
	return out
}

// Message decodes one raw session_message row.
func Message(r store.RawMessage) model.Message {
	m := model.Message{
		ID: r.ID, Seq: r.Seq, Type: r.Type,
		Created: nullMs(r.Created), Updated: nullMs(r.Updated),
	}
	if !r.Valid {
		m.Malformed = true
		m.RawPreview = r.RawPreview.String
		return m
	}
	m.Agent = r.Agent.String
	m.Model = ModelRef(raw(r.Model))
	if r.Cost.Valid {
		c := r.Cost.Float64
		m.Cost = &c
	}
	m.Tokens = Tokens(raw(r.Tokens))
	m.Finish = r.Finish.String
	m.Err = StepError(raw(r.Error))
	m.Retry = retry(raw(r.Retry))
	m.Time = stepTime(raw(r.Time))
	if m.Time.Created.IsZero() {
		m.Time.Created = m.Created
	}
	m.Outcome = r.Outcome.String
	m.Status = r.Status.String
	m.Reason = r.Reason.String
	m.Previous = raw(r.Previous)
	m.Description = r.Description.String
	m.Metadata = raw(r.Metadata)
	m.Text = r.TextPreview.String
	m.TextLen = int(r.TextLen.Int64)
	m.Agents = strList(r.Agents)
	m.Skills = strList(r.Skills)
	if r.Files.Valid {
		var fs []struct {
			Name string `json:"name"`
			Mime string `json:"mime"`
			Size int    `json:"size"`
		}
		if json.Unmarshal([]byte(r.Files.String), &fs) == nil {
			for _, f := range fs {
				m.Files = append(m.Files, model.FileRef{Name: f.Name, Mime: f.Mime, Size: f.Size})
			}
		}
	}
	switch r.Type {
	case "skill":
		if m.Text == "" {
			m.Text = r.SkillName.String
		}
		m.Description = r.SkillName.String
	case "shell":
		m.Text = r.ShellCommand.String
	case "model-switched":
		m.Model = ModelRef(raw(r.ModelSwitchTarget))
	case "location-switched":
		m.Text = r.Location.String
	}
	if !store.KnownMessageTypes[r.Type] {
		// Keep top-level keys so Unknown rows can say what they contain.
		for _, k := range strList(r.Keys) {
			if m.Extra == nil {
				m.Extra = map[string]json.RawMessage{}
			}
			m.Extra[k] = nil
		}
	}
	return m
}

// Item decodes one raw content item.
func Item(r store.RawItem) model.ItemSummary {
	it := model.ItemSummary{
		Index: r.Index, Kind: r.Type.String, ToolName: r.Name.String, ToolCallID: r.CallID.String,
		Status: r.Status.String, TextPreview: r.TextPreview.String, TextLen: int(r.TextLen.Int64),
		Encrypted: r.Encrypted, Err: StepError(raw(r.Error)),
		ChildSession: r.ChildSession.String, ChildAgent: r.ChildAgent.String,
		ChildModel: ModelRef(raw(r.ChildModel)),
		Loaded:     strList(r.Loaded), Files: strList(r.Files),
	}
	if it.Kind == "" {
		it.Kind = "other"
	}
	t := stepTime(raw(r.Time))
	it.Start, it.End = t.Created, t.Completed
	it.InputPreview = inputPreview(it.ToolName, r.Input.String)
	return it
}

// inputPreview picks the most informative field of a tool input for one-line display.
func inputPreview(tool, in string) string {
	if in == "" {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal([]byte(in), &obj) != nil {
		// Truncated JSON (capped by SQL) or a plain string; show as-is.
		return oneLine(in)
	}
	for _, k := range []string{"description", "command", "filePath", "path", "pattern", "url", "query", "name", "prompt"} {
		if v, ok := obj[k].(string); ok && v != "" {
			if k == "description" && tool != "task" && tool != "subagent" {
				if c, ok := obj["command"].(string); ok && c != "" {
					return oneLine(c)
				}
			}
			return oneLine(v)
		}
	}
	return oneLine(in)
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// ToolDetail decodes a full tool content item JSON (lazy detail).
func ToolDetail(js string) model.ToolDetail {
	var t struct {
		State struct {
			Input    json.RawMessage `json:"input"`
			Content  json.RawMessage `json:"content"`
			Output   string          `json:"output"` // legacy
			Error    json.RawMessage `json:"error"`
			Metadata json.RawMessage `json:"metadata"`
		} `json:"state"`
	}
	var d model.ToolDetail
	if json.Unmarshal([]byte(js), &t) != nil {
		d.Output = js
		return d
	}
	d.Input = t.State.Input
	d.Metadata = t.State.Metadata
	d.Err = StepError(t.State.Error)
	d.Output = contentText(t.State.Content)
	if d.Output == "" {
		d.Output = t.State.Output
	}
	var md struct {
		Diff      string `json:"diff"`
		Truncated bool   `json:"truncated"`
		Output    string `json:"output"`
		Files     []struct {
			Diff string `json:"diff"`
		} `json:"files"`
	}
	if json.Unmarshal(t.State.Metadata, &md) == nil {
		d.Diff = md.Diff
		if d.Diff == "" {
			var parts []string
			for _, f := range md.Files {
				if f.Diff != "" {
					parts = append(parts, f.Diff)
				}
			}
			d.Diff = strings.Join(parts, "\n")
		}
		d.Truncated = md.Truncated
		if d.Output == "" {
			d.Output = md.Output
		}
	}
	return d
}

// contentText joins [{type:"text", text}] arrays; non-text entries are summarized.
func contentText(b json.RawMessage) string {
	if len(b) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		return s
	}
	var items []map[string]any
	if json.Unmarshal(b, &items) != nil {
		return string(b)
	}
	var sb strings.Builder
	for i, it := range items {
		if i > 0 {
			sb.WriteString("\n")
		}
		if t, ok := it["text"].(string); ok {
			sb.WriteString(t)
			continue
		}
		typ, _ := it["type"].(string)
		mime, _ := it["mime"].(string)
		sb.WriteString("[" + strings.TrimSpace(typ+" "+mime) + " attachment]")
	}
	return sb.String()
}

// ItemText decodes the text of a text/reasoning content item.
func ItemText(js string) (text string, encrypted bool) {
	var t struct {
		Text  string `json:"text"`
		State struct {
			Enc string `json:"reasoningEncryptedContent"`
		} `json:"state"`
	}
	if json.Unmarshal([]byte(js), &t) != nil {
		return js, false
	}
	return t.Text, t.State.Enc != ""
}
