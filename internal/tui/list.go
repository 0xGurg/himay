package tui

import (
	"strings"

	"github.com/0xGurg/himay/internal/textutil"
)

// cursor is a scrolling selection over n items within a viewport of h rows.
type cursor struct {
	pos, off int
}

func (c *cursor) clamp(n, h int) {
	if n == 0 {
		c.pos, c.off = 0, 0
		return
	}
	c.pos = max(0, min(c.pos, n-1))
	if h <= 0 {
		return
	}
	if c.pos < c.off {
		c.off = c.pos
	}
	if c.pos >= c.off+h {
		c.off = c.pos - h + 1
	}
	c.off = max(0, min(c.off, max(0, n-h)))
}

// move handles common navigation keys; it returns false if the key is not a movement.
func (c *cursor) move(key string, n, h int) bool {
	switch key {
	case "j", "down":
		c.pos++
	case "k", "up":
		c.pos--
	case "g", "home":
		c.pos = 0
	case "G", "end":
		c.pos = n - 1
	case "ctrl+d", "pgdown":
		c.pos += max(1, h/2)
	case "ctrl+u", "pgup":
		c.pos -= max(1, h/2)
	default:
		return false
	}
	c.clamp(n, h)
	return true
}

// window renders items [off, off+h) using render(i, selected).
func (c *cursor) window(n, h int, render func(i int, selected bool) string) string {
	c.clamp(n, h)
	var b strings.Builder
	for i := c.off; i < min(n, c.off+h); i++ {
		if i > c.off {
			b.WriteByte('\n')
		}
		b.WriteString(render(i, i == c.pos))
	}
	return b.String()
}

// box draws a titled rounded panel of outer size w×h around content.
func (a *App) box(title, content string, w, h int, focused bool) string {
	bc := a.s.faint
	if focused {
		bc = a.s.info
	}
	w, h = max(w, 4), max(h, 3)
	iw, ih := w-2, h-2
	t := ""
	if title != "" {
		t = textutil.Truncate(" "+title+" ", max(0, iw-2))
	}
	topFill := iw - 1 - textutil.Width(t)
	var b strings.Builder
	b.WriteString(bc.Render("╭─"))
	if focused {
		b.WriteString(a.s.bold.Render(t))
	} else {
		b.WriteString(a.s.dim.Render(t))
	}
	b.WriteString(bc.Render(strings.Repeat("─", max(0, topFill)) + "╮"))
	side := bc.Render("│")
	for _, l := range strings.Split(fit(content, iw, ih), "\n") {
		b.WriteString("\n" + side + textutil.Pad(l, iw) + side)
	}
	b.WriteString("\n" + bc.Render("╰"+strings.Repeat("─", iw)+"╯"))
	return b.String()
}
