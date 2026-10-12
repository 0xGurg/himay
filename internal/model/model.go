// Package model holds Himay's domain types. It has no knowledge of SQL or the TUI.
package model

import (
	"encoding/json"
	"time"
)

// ModelRef identifies an LLM. The zero value means "unknown".
type ModelRef struct {
	ID         string `json:"id"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant,omitempty"`
}

func (m ModelRef) Known() bool { return m.ID != "" }

func (m ModelRef) String() string {
	if !m.Known() {
		return "—"
	}
	s := m.ID
	if m.ProviderID != "" {
		s = m.ProviderID + "/" + s
	}
	if m.Variant != "" && m.Variant != "default" {
		s += ":" + m.Variant
	}
	return s
}

// Tokens is token usage. Known is false when OpenCode recorded nothing.
type Tokens struct {
	Input, Output, Reasoning, CacheRead, CacheWrite int64
	Known                                           bool
}

func (t Tokens) Total() int64 { return t.Input + t.Output + t.Reasoning + t.CacheRead + t.CacheWrite }

func (t *Tokens) Add(o Tokens) {
	if !o.Known {
		return
	}
	t.Known = true
	t.Input += o.Input
	t.Output += o.Output
	t.Reasoning += o.Reasoning
	t.CacheRead += o.CacheRead
	t.CacheWrite += o.CacheWrite
}

type Project struct {
	ID         string
	Worktree   string
	Name       string
	Sessions   int
	LastActive time.Time
}

type SessionSummary struct {
	ID, ProjectID, ParentID string
	Title, Directory        string
	Agent, Version          string
	Model                   ModelRef
	Cost                    float64
	Tokens                  Tokens
	Created, Updated        time.Time
	Archived                *time.Time
	Children                int
}

// StepError is an assistant-step or tool error as stored by OpenCode.
type StepError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type RetryInfo struct {
	Attempt int64
	At      time.Time
	Error   string
}

// StepTime holds assistant step timestamps; zero values mean "not recorded".
type StepTime struct {
	Created, Streamed, Completed time.Time
}

// Message is one session_message row with its large payloads summarized.
type Message struct {
	ID      string
	Seq     int64
	Type    string
	Created time.Time
	Updated time.Time

	// assistant
	Agent  string
	Model  ModelRef
	Cost   *float64
	Tokens Tokens
	Finish string
	Err    *StepError
	Retry  *RetryInfo
	Time   StepTime
	Items  []ItemSummary

	// user / system / synthetic / skill
	Text        string // preview only
	TextLen     int
	Description string
	Agents      []string // user @agent mentions
	Skills      []string // user skill mentions
	Files       []FileRef

	// switches / idle / compaction / shell
	Previous   json.RawMessage
	Outcome    string
	Status     string
	Reason     string
	Metadata   json.RawMessage
	Extra      map[string]json.RawMessage // anything else, for Unknown rendering
	Malformed  bool
	RawPreview string
}

type FileRef struct {
	Name, Mime string
	Size       int
}

// ItemSummary summarizes one entry of an assistant message's content array.
type ItemSummary struct {
	Index        int
	Kind         string // text | reasoning | tool | other
	ToolName     string
	ToolCallID   string
	Status       string
	InputPreview string
	TextPreview  string
	TextLen      int
	Encrypted    bool
	Err          *StepError
	Start, End   time.Time
	ChildSession string
	ChildAgent   string
	ChildModel   ModelRef
	Loaded       []string // AGENTS.md files auto-attached by read
	Files        []string // file paths touched (edit/write/apply_patch)
}

// ToolDetail is the full payload of one tool call, loaded lazily.
type ToolDetail struct {
	Input     json.RawMessage
	Output    string
	Truncated bool
	Diff      string
	Metadata  json.RawMessage
	Err       *StepError
}

// Evidence labels how strongly a fact is supported.
type Evidence uint8

const (
	Recorded Evidence = iota
	Strong
	Possible
	Unknown
	Derived
)

func (e Evidence) String() string {
	return [...]string{"recorded", "strong", "possible", "unknown", "derived"}[e]
}

// InstructionRecord is a piece of instruction provenance.
type InstructionRecord struct {
	Kind     string // agents_md | skill | system | tools | env | date | codemode | skill_list | mcp | other
	Key      string
	Path     string
	Name     string
	MsgID    string
	Item     int
	Seq      int64
	Hash     string
	Removed  bool
	Preview  string
	Evidence Evidence
	Source   string // human-readable origin, e.g. "instruction_state.initial", "read tool metadata.loaded"
}
