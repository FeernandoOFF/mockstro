package tui

import tea "github.com/charmbracelet/bubbletea"

// Shared keymap: x/X destroy (item/all), u/d half page, g/G ends, enter/y confirm, esc dismiss.
const (
	pageSize    = 10
	navHelp     = "↑↓/u/d/g/G move"
	confirmHint = "[enter/y · esc]"
)

func confirmed(k tea.KeyMsg) bool {
	s := k.String()
	return s == "y" || s == "Y" || s == "enter"
}

// navKey maps a list-navigation key to a new cursor in [0, n).
func navKey(k string, cur, n int) (int, bool) {
	switch k {
	case "up", "k":
		cur--
	case "down", "j":
		cur++
	case "u":
		cur -= pageSize
	case "d":
		cur += pageSize
	case "g", "home":
		cur = 0
	case "G", "end":
		cur = n - 1
	default:
		return cur, false
	}
	return max(0, min(cur, n-1)), true
}
