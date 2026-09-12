package ui

import "github.com/charmbracelet/lipgloss"

// Glyphs used in the table. Lipgloss measures display width correctly for
// these, so unlike the shell version there is no locale-dependent padding to
// work around.
const (
	glyphUp     = "✔"
	glyphDown   = "✘"
	glyphBetter = "▼"
	glyphWorse  = "▲"
	glyphNone   = "—"
	glyphFlash  = "▌"
)

// A restrained palette: status carries the only saturated colour, so a table
// of healthy hosts reads as calm and a failure is the thing your eye lands on.
// Adaptive pairs keep it legible on light and dark terminals alike.
var (
	colText   = lipgloss.AdaptiveColor{Light: "236", Dark: "252"}
	colMuted  = lipgloss.AdaptiveColor{Light: "245", Dark: "243"}
	colFaint  = lipgloss.AdaptiveColor{Light: "250", Dark: "238"}
	colUp     = lipgloss.AdaptiveColor{Light: "28", Dark: "42"}
	colDown   = lipgloss.AdaptiveColor{Light: "160", Dark: "203"}
	colAccent = lipgloss.AdaptiveColor{Light: "25", Dark: "75"}
	colWarn   = lipgloss.AdaptiveColor{Light: "130", Dark: "215"}
)

// flashRamp fades the marker on a row whose host has just come alive, from
// freshest at index 0 to nearly gone at the end. Motion is what draws the eye
// here, so the ramp can stay quiet rather than shouting in saturated green.
var flashRamp = [5]lipgloss.AdaptiveColor{
	{Light: "22", Dark: "46"},
	{Light: "28", Dark: "41"},
	{Light: "35", Dark: "35"},
	{Light: "71", Dark: "29"},
	{Light: "151", Dark: "23"},
}

var (
	styleTitle   = lipgloss.NewStyle().Bold(true).Foreground(colAccent)
	styleHint    = lipgloss.NewStyle().Foreground(colMuted)
	styleHeader  = lipgloss.NewStyle().Bold(true).Foreground(colMuted)
	styleRule    = lipgloss.NewStyle().Foreground(colFaint)
	styleHost    = lipgloss.NewStyle().Foreground(colText)
	styleUp      = lipgloss.NewStyle().Foreground(colUp)
	styleDown    = lipgloss.NewStyle().Foreground(colDown).Bold(true)
	styleWait    = lipgloss.NewStyle().Foreground(colMuted)
	styleValue   = lipgloss.NewStyle().Foreground(colText)
	styleMuted   = lipgloss.NewStyle().Foreground(colMuted)
	styleFaint   = lipgloss.NewStyle().Foreground(colFaint)
	styleBetter  = lipgloss.NewStyle().Foreground(colUp)
	styleWorse   = lipgloss.NewStyle().Foreground(colDown)
	styleWarn    = lipgloss.NewStyle().Foreground(colWarn)
	styleFooter  = lipgloss.NewStyle().Foreground(colMuted)
	styleKeyName = lipgloss.NewStyle().Bold(true).Foreground(colText)
)
