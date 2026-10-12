// Package provenance extracts recorded instruction evidence from a session and
// computes clearly-labeled heuristic correlations with later actions.
//
// Extraction only reports what OpenCode stored (Evidence = Recorded).
// Correlation never upgrades anything to Recorded.
package provenance

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/store"
)

// Source is an instruction with its full text loader.
type Source struct {
	model.InstructionRecord
	Text string // full text when cheaply available; otherwise loaded via Load
}

// sectionKind maps instruction_state section keys to kinds.
func sectionKind(key string) string {
	switch key {
	case "core/instructions":
		return "agents_md"
	case "core/environment":
		return "env"
	case "core/date":
		return "date"
	case "core/skill-guidance":
		return "skill_list"
	case "core/codemode":
		return "tool_catalog"
	case "core/mcp-guidance":
		return "mcp"
	case "core/reference-guidance":
		return "reference"
	}
	return "other"
}

// Extract gathers every recorded instruction fact for a session.
func Extract(ctx context.Context, st *store.Store, sessionID string, msgs []model.Message) ([]Source, error) {
	var out []Source

	state, entries, err := st.Instructions(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if state != nil {
		addState := func(vals map[string]string, which string, seq int64) {
			keys := make([]string, 0, len(vals))
			for k := range vals {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				h := vals[k]
				src := Source{InstructionRecord: model.InstructionRecord{
					Kind: sectionKind(k), Key: k, Hash: h, Seq: seq, Item: -1,
					Removed: h == "removed", Evidence: model.Recorded,
					Source: "instruction_state." + which,
				}}
				if !src.Removed {
					txt, err := st.InstructionBlob(ctx, h)
					if err != nil {
						continue
					}
					src.Text = txt
					src.Preview = preview(txt)
					if src.Kind == "agents_md" {
						src.Path = strings.Join(instructionPaths(txt), ", ")
					}
				}
				out = append(out, src)
			}
		}
		addState(state.Initial, "initial", state.EpochStart)
		// Only report current values that differ from the initial snapshot.
		changed := map[string]string{}
		for k, v := range state.Current {
			if state.Initial[k] != v {
				changed[k] = v
			}
		}
		addState(changed, "current", state.ThroughSeq)
	}
	for _, e := range entries {
		out = append(out, Source{InstructionRecord: model.InstructionRecord{
			Kind: sectionKind(e.Key), Key: e.Key, Removed: e.Removed, Item: -1,
			Preview: preview(e.Value), Evidence: model.Recorded, Source: "instruction_entry",
		}, Text: e.Value})
	}

	for _, m := range msgs {
		switch m.Type {
		case "system":
			kind := "system"
			if strings.HasPrefix(m.Text, "The available tools have changed") {
				kind = "tools"
			} else if isInstructionNotice(m.Metadata) || strings.HasPrefix(m.Text, "Instructions") {
				kind = "agents_md_update"
			}
			out = append(out, Source{InstructionRecord: model.InstructionRecord{
				Kind: kind, MsgID: m.ID, Seq: m.Seq, Item: -1, Preview: preview(m.Text),
				Evidence: model.Recorded, Source: "system message",
			}})
		case "synthetic":
			paths := syntheticPaths(m.Metadata)
			if len(paths) == 0 && !strings.HasPrefix(m.Text, "Instructions from:") {
				continue
			}
			out = append(out, Source{InstructionRecord: model.InstructionRecord{
				Kind: "agents_md", Path: strings.Join(paths, ", "), MsgID: m.ID, Seq: m.Seq, Item: -1,
				Preview: preview(m.Text), Evidence: model.Recorded, Source: "synthetic message (nested load)",
			}})
		case "skill":
			out = append(out, Source{InstructionRecord: model.InstructionRecord{
				Kind: "skill", Name: m.Description, MsgID: m.ID, Seq: m.Seq, Item: -1,
				Preview: preview(m.Text), Evidence: model.Recorded, Source: "skill message",
			}})
		case "user":
			for _, s := range m.Skills {
				out = append(out, Source{InstructionRecord: model.InstructionRecord{
					Kind: "skill_mention", Name: s, MsgID: m.ID, Seq: m.Seq, Item: -1,
					Evidence: model.Recorded, Source: "user skill mention",
				}})
			}
			for _, a := range m.Agents {
				out = append(out, Source{InstructionRecord: model.InstructionRecord{
					Kind: "agent_mention", Name: a, MsgID: m.ID, Seq: m.Seq, Item: -1,
					Evidence: model.Recorded, Source: "user @agent mention",
				}})
			}
		case "assistant":
			for _, it := range m.Items {
				if it.Kind != "tool" {
					continue
				}
				for _, p := range it.Loaded {
					out = append(out, Source{InstructionRecord: model.InstructionRecord{
						Kind: "agents_md", Path: p, MsgID: m.ID, Item: it.Index, Seq: m.Seq,
						Evidence: model.Recorded, Source: "read tool metadata.loaded",
					}})
				}
				switch {
				case it.ToolName == "skill":
					out = append(out, Source{InstructionRecord: model.InstructionRecord{
						Kind: "skill", Name: it.InputPreview, MsgID: m.ID, Item: it.Index, Seq: m.Seq,
						Evidence: model.Recorded, Source: "skill tool call",
					}})
				case isInstructionFile(it.InputPreview) && (it.ToolName == "read" || it.ToolName == "edit" || it.ToolName == "write"):
					// A file action on an instruction file. Recorded as an action, not as a load.
					out = append(out, Source{InstructionRecord: model.InstructionRecord{
						Kind: "instruction_file_action", Path: it.InputPreview, Name: it.ToolName,
						MsgID: m.ID, Item: it.Index, Seq: m.Seq,
						Evidence: model.Recorded, Source: it.ToolName + " tool on instruction file",
					}})
				}
			}
		}
	}
	return out, nil
}

// Load fetches the full text of a source whose Text is not yet populated.
func Load(ctx context.Context, st *store.Store, s *Source) error {
	if s.Text != "" || s.Removed {
		return nil
	}
	if s.MsgID == "" {
		return nil
	}
	if s.Item < 0 {
		t, err := st.MessageText(ctx, s.MsgID)
		s.Text = t
		return err
	}
	js, err := st.ItemJSON(ctx, s.MsgID, s.Item)
	if err != nil {
		return err
	}
	var item struct {
		State struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"state"`
	}
	if json.Unmarshal([]byte(js), &item) != nil {
		return nil
	}
	var sb strings.Builder
	for _, c := range item.State.Content {
		sb.WriteString(c.Text)
	}
	full := sb.String()
	if s.Kind == "agents_md" && s.Path != "" {
		s.Text = Section(full, s.Path)
	} else {
		s.Text = full
	}
	return nil
}

// Section extracts the "Instructions from: <path>" block within a read output.
func Section(text, path string) string {
	marker := "Instructions from: " + path
	i := strings.Index(text, marker)
	if i < 0 {
		return ""
	}
	rest := text[i+len(marker):]
	if j := strings.Index(rest, "</system-reminder>"); j >= 0 {
		rest = rest[:j]
	}
	if j := strings.Index(rest, "Instructions from: "); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}

func instructionPaths(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if p, ok := strings.CutPrefix(strings.TrimSpace(line), "Instructions from: "); ok {
			out = append(out, strings.TrimSpace(p))
		}
	}
	return out
}

func isInstructionNotice(md json.RawMessage) bool {
	var m struct {
		Notice string `json:"notice"`
	}
	return json.Unmarshal(md, &m) == nil && m.Notice == "instructions"
}

func syntheticPaths(md json.RawMessage) []string {
	var m struct {
		Instruction struct {
			Paths []string `json:"paths"`
		} `json:"instruction"`
	}
	_ = json.Unmarshal(md, &m)
	return m.Instruction.Paths
}

func isInstructionFile(p string) bool {
	return strings.HasSuffix(p, "AGENTS.md") || strings.HasSuffix(p, "SKILL.md") ||
		strings.HasSuffix(p, "CLAUDE.md") ||
		(strings.Contains(p, "/agents/") && strings.HasSuffix(p, ".md"))
}

func preview(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return s
}
