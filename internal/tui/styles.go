package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	colAccent = lipgloss.AdaptiveColor{Light: "#5B4BDB", Dark: "#A59BFF"}
	colMuted  = lipgloss.AdaptiveColor{Light: "#6B6B76", Dark: "#8A8A96"}
	colOK     = lipgloss.AdaptiveColor{Light: "#1A7F37", Dark: "#56D364"}
	colBad    = lipgloss.AdaptiveColor{Light: "#CF222E", Dark: "#FF7B72"}
	colWarn   = lipgloss.AdaptiveColor{Light: "#9A6700", Dark: "#E3B341"}
	colBorder = lipgloss.AdaptiveColor{Light: "#D0D0D8", Dark: "#3A3A46"}

	sTitle      = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	sMuted      = lipgloss.NewStyle().Foreground(colMuted)
	sOK         = lipgloss.NewStyle().Foreground(colOK)
	sBad        = lipgloss.NewStyle().Foreground(colBad)
	sWarn       = lipgloss.NewStyle().Foreground(colWarn)
	sBold       = lipgloss.NewStyle().Bold(true)
	sSel        = lipgloss.NewStyle().Foreground(colAccent).Bold(true)
	sTab        = lipgloss.NewStyle().Padding(0, 1).Foreground(colMuted)
	sTabActive  = lipgloss.NewStyle().Padding(0, 1).Foreground(colAccent).Bold(true).Underline(true)
	sPanel      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colBorder).Padding(0, 1)
	sPanelFocus = sPanel.BorderForeground(colAccent)
	sHelp       = lipgloss.NewStyle().Foreground(colMuted)
)

func panel(title, body string, w, h int) string { return panelStyled(sPanel, title, body, w, h) }

// focusPanel is a panel whose border lights up while it has keyboard focus.
func focusPanel(focused bool, title, body string, w, h int) string {
	if focused {
		return panelStyled(sPanelFocus, title, body, w, h)
	}
	return panel(title, body, w, h)
}

func panelStyled(st lipgloss.Style, title, body string, w, h int) string {
	if w < 10 {
		w = 10
	}
	if h < 3 {
		h = 3
	}
	inner := h - 3
	lines := strings.Split(body, "\n")
	if len(lines) > inner {
		lines = lines[:inner]
	}
	for i, l := range lines {
		lines[i] = truncate(l, w-4)
	}
	content := sTitle.Render(title) + "\n" + strings.Join(lines, "\n")
	return st.Width(w - 2).Height(h - 2).Render(content)
}

func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	// ANSI-aware: keeps colours on the part that fits.
	return ansi.Truncate(s, w, "…")
}
func statusStyle(pass bool, stage string) string {
	switch t := statusText(pass, stage); t {
	case "PASS":
		return sOK.Render(t)
	case "FAIL":
		return sBad.Render(t)
	default:
		return sWarn.Render(t)
	}
}

func statusText(pass bool, stage string) string {
	switch {
	case pass:
		return "PASS"
	case stage == "UNSTARTED", stage == "INTERRUPTED", stage == "CANCELLED":
		return stage
	}
	return "FAIL"
}

// window returns the [start,end) slice of n items keeping cursor visible in height rows.
func window(n, cursor, height int) (int, int) {
	if height <= 0 || n <= height {
		return 0, n
	}
	start := cursor - height/2
	if start < 0 {
		start = 0
	}
	if start+height > n {
		start = n - height
	}
	return start, start + height
}

func pad(s string, w int) string {
	if gap := w - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return truncate(s, w)
}

// visibleLines keeps line `focus` on screen within height rows, returning the lines to draw.
func visibleLines(lines []string, focus, height int) []string {
	if height <= 0 || len(lines) <= height {
		return lines
	}
	start := min(max(0, focus-height/2), len(lines)-height)
	return lines[start : start+height]
}
