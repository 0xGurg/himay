package tui

import (
	"image/color"

	"charm.land/lipgloss/v2"

	"github.com/0xGurg/himay/internal/model"
	"github.com/0xGurg/himay/internal/trace"
)

// TitleBaybayin is "Himay" in Baybayin: HA + I (U+1712) · MA · YA + virama (U+1714).
const TitleBaybayin = "ᜑᜒᜋᜌ᜔"

type styles struct {
	title, dim, faint, bold, sel, selDim, border, borderFocus lipgloss.Style
	ok, fail, warn, info, accent, user, step, reasoning, tool lipgloss.Style
	evRecorded, evStrong, evPossible, evUnknown, evDerived    lipgloss.Style
	diffAdd, diffDel, diffHunk                                lipgloss.Style
}

func newStyles(dark bool) styles {
	ld := lipgloss.LightDark(dark)
	c := func(light, darkHex string) color.Color { return ld(lipgloss.Color(light), lipgloss.Color(darkHex)) }
	fg := lipgloss.NewStyle().Foreground
	selBg := c("#dde4f0", "#2a3550")
	return styles{
		title:       lipgloss.NewStyle().Bold(true).Foreground(c("#7a3e00", "#f0b45a")),
		dim:         fg(c("#6b6b6b", "#8a8a8a")),
		faint:       fg(c("#9a9a9a", "#5c5c5c")),
		bold:        lipgloss.NewStyle().Bold(true),
		sel:         lipgloss.NewStyle().Background(selBg).Bold(true),
		selDim:      lipgloss.NewStyle().Background(c("#eceff4", "#1f2433")),
		border:      lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c("#c8c8c8", "#3a3a3a")),
		borderFocus: lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(c("#4a6fa5", "#6f9ce0")),
		ok:          fg(c("#2e7d32", "#7ccf7f")),
		fail:        fg(c("#c62828", "#ff7b72")),
		warn:        fg(c("#a66300", "#e3b341")),
		info:        fg(c("#1f5fa8", "#79b8ff")),
		accent:      fg(c("#7b1fa2", "#d2a8ff")),
		user:        lipgloss.NewStyle().Bold(true).Foreground(c("#00695c", "#56d4c0")),
		step:        lipgloss.NewStyle().Bold(true).Foreground(c("#37474f", "#c9d1d9")),
		reasoning:   fg(c("#6a4c93", "#b392f0")),
		tool:        fg(c("#1f5fa8", "#79b8ff")),
		evRecorded:  fg(c("#2e7d32", "#7ccf7f")).Bold(true),
		evStrong:    fg(c("#a66300", "#f0b45a")).Bold(true),
		evPossible:  fg(c("#8d6e00", "#e3d341")),
		evUnknown:   fg(c("#9a9a9a", "#6e7681")),
		evDerived:   fg(c("#5c6bc0", "#a5b4fc")),
		diffAdd:     fg(c("#2e7d32", "#7ccf7f")),
		diffDel:     fg(c("#c62828", "#ff7b72")),
		diffHunk:    fg(c("#1f5fa8", "#79b8ff")),
	}
}

func (s styles) evidence(e model.Evidence) string {
	t := "[" + e.String() + "]"
	switch e {
	case model.Recorded:
		return s.evRecorded.Render(t)
	case model.Strong:
		return s.evStrong.Render(t)
	case model.Possible:
		return s.evPossible.Render(t)
	case model.Derived:
		return s.evDerived.Render(t)
	}
	return s.evUnknown.Render(t)
}

func (s styles) status(st trace.Status) string {
	switch st {
	case trace.SOK:
		return s.ok.Render("✓")
	case trace.SFail:
		return s.fail.Render("✗")
	case trace.SRunning:
		return s.warn.Render("…")
	}
	return " "
}

func (s styles) kind(k trace.Kind) lipgloss.Style {
	switch k {
	case trace.KUser:
		return s.user
	case trace.KStep:
		return s.step
	case trace.KReasoning:
		return s.reasoning
	case trace.KReasoningUnavailable, trace.KSystem, trace.KSynthetic, trace.KIdle:
		return s.dim
	case trace.KTool, trace.KShell:
		return s.tool
	case trace.KSubagent, trace.KSkill, trace.KInstructionLoaded, trace.KInstructionUpdated:
		return s.accent
	case trace.KModelSwitched, trace.KAgentSwitched, trace.KLocationSwitched, trace.KToolsChanged, trace.KCompaction:
		return s.warn
	case trace.KError, trace.KRetry:
		return s.fail
	case trace.KFileChange:
		return s.ok
	}
	return s.dim
}
