package trace

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// Glyph returns the one-character marker for a row kind.
func Glyph(k Kind) string {
	switch k {
	case KUser:
		return "▶"
	case KStep:
		return "◆"
	case KText:
		return "¶"
	case KReasoning, KReasoningUnavailable:
		return "∴"
	case KTool:
		return "⚙"
	case KSubagent:
		return "↳"
	case KFileChange:
		return "±"
	case KShell:
		return "$"
	case KSkill:
		return "✦"
	case KInstructionLoaded, KInstructionUpdated:
		return "§"
	case KToolsChanged:
		return "⚒"
	case KSystem, KSynthetic:
		return "·"
	case KModelSwitched, KAgentSwitched, KLocationSwitched:
		return "⇄"
	case KRetry:
		return "↻"
	case KCompaction:
		return "⊜"
	case KIdle:
		return "◌"
	case KError:
		return "✗"
	}
	return "?"
}

func StatusMark(s Status) string {
	switch s {
	case SOK:
		return "✓"
	case SFail:
		return "✗"
	case SRunning:
		return "…"
	}
	return " "
}

// Elapsed formats t relative to the session start as m:ss or h:mm:ss.
func Elapsed(t, start time.Time) string {
	if t.IsZero() || start.IsZero() {
		return "     "
	}
	d := t.Sub(start)
	if d < 0 {
		d = 0
	}
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h >= 24 {
		return fmt.Sprintf("%dd%02d:%02d", h/24, h%24, m)
	}
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func Duration(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return d.Round(time.Second).String()
}

// Plain renders a row as one line of plain text (used by dump and golden tests).
func Plain(r Row, start time.Time) string {
	var b strings.Builder
	b.WriteString(Elapsed(r.Time, start))
	b.WriteString(" ")
	b.WriteString(strings.Repeat("  ", r.Depth))
	b.WriteString(Glyph(r.Kind))
	b.WriteString(" ")
	b.WriteString(StatusMark(r.Status))
	b.WriteString(" ")
	b.WriteString(fmt.Sprintf("%-14s", r.Kind))
	if r.Kind == KStep {
		b.WriteString(r.Agent + " · " + r.Model.String() + "  ")
	}
	b.WriteString(r.Label)
	if d := Duration(r.Dur); d != "" {
		b.WriteString("  [" + d + "]")
	}
	if r.Child != "" {
		b.WriteString("  child=" + r.Child)
	}
	if r.Evidence != 0 {
		b.WriteString("  [" + r.Evidence.String() + "]")
	}
	return b.String()
}

func WritePlain(w io.Writer, rows []Row, start time.Time) error {
	for _, r := range rows {
		if _, err := fmt.Fprintln(w, Plain(r, start)); err != nil {
			return err
		}
	}
	return nil
}
