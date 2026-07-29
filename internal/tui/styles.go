package tui

import (
	"github.com/Bakaface/sortie/internal/task"
	"github.com/charmbracelet/lipgloss"
)

// AppTitle is the display title for Sortie, with an airplane (✈) reflecting
// the aviation origin of the word "sortie" — a short combat mission.
const AppTitle = "✈ Sortie"

// PromptPrefix is the airplane character used as a prompt prefix for task input,
// similar to Claude Code's ❯ character.
const PromptPrefix = "✈ "

// promptColor is the foreground color for the ✈ prompt prefix in the textarea.
// Animation planes must use the same color for visual continuity.
var promptColor = lipgloss.Color("#E8E8E8")

var (
	subtle    = lipgloss.AdaptiveColor{Light: "#D9DCCF", Dark: "#3A3A3A"}
	highlight = lipgloss.AdaptiveColor{Light: "#3D6E99", Dark: "#5F8AB3"}

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#E8E8E8")).
			Background(highlight).
			Padding(0, 1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(highlight).
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(subtle)

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#E8E8E8")).
			Background(highlight)

	normalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E8E8E8"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B6B6B"))

	// stateStyles is keyed by task.Status so adding a new status forces every
	// caller (stateStyle, statusIconFor) to be reconsidered at compile time.
	// task.StatusAwaitingChildren has no entry (falls back to normalStyle via
	// stateStyle) — a pre-existing gap, preserved as-is rather than fixed here.
	stateStyles = map[task.Status]lipgloss.Style{
		task.StatusPending:            lipgloss.NewStyle().Foreground(lipgloss.Color("#6B6B6B")),
		task.StatusInit:               lipgloss.NewStyle().Foreground(lipgloss.Color("#D4A843")).Italic(true),
		task.StatusRunning:            lipgloss.NewStyle().Foreground(lipgloss.Color("#D4A843")),
		task.StatusAwaitingApproval:   lipgloss.NewStyle().Foreground(lipgloss.Color("#D4A843")).Bold(true),
		task.StatusTmux:               lipgloss.NewStyle().Foreground(lipgloss.Color("#B07AAD")).Bold(true),
		task.StatusFinalizing:         lipgloss.NewStyle().Foreground(lipgloss.Color("#5F8AB3")),
		task.StatusSummarizing:        lipgloss.NewStyle().Foreground(lipgloss.Color("#5F8AB3")),
		task.StatusSummarizingStep:    lipgloss.NewStyle().Foreground(lipgloss.Color("#5F8AB3")),
		task.StatusMergeBlocked:       lipgloss.NewStyle().Foreground(lipgloss.Color("#C97054")).Bold(true),
		task.StatusResolvingConflicts: lipgloss.NewStyle().Foreground(lipgloss.Color("#C97054")).Bold(true),
		task.StatusCompleted:          lipgloss.NewStyle().Foreground(lipgloss.Color("#5BA87A")),
		task.StatusFailed:             lipgloss.NewStyle().Foreground(lipgloss.Color("#D94F4F")),
		task.StatusMergeFailed:        lipgloss.NewStyle().Foreground(lipgloss.Color("#D94F4F")).Bold(true),
	}

	priorityStyles = map[string]lipgloss.Style{
		"low":    lipgloss.NewStyle().Foreground(lipgloss.Color("#6B6B6B")),
		"medium": lipgloss.NewStyle().Foreground(lipgloss.Color("#5F8AB3")),
		"high":   lipgloss.NewStyle().Foreground(lipgloss.Color("#D4A843")),
		"urgent": lipgloss.NewStyle().Foreground(lipgloss.Color("#D94F4F")).Bold(true),
	}

	lineNumStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#6B6B6B"))

	helpStyle = lipgloss.NewStyle().
			Foreground(dimStyle.GetForeground())

	viewportStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(subtle).
			Padding(0, 1)

	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.AdaptiveColor{Light: "#3A3A3A", Dark: "#B0B0A8"}).
			Background(lipgloss.AdaptiveColor{Light: "#D9DCCF", Dark: "#333333"})

	searchMatchStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("#3A3A3A")).
				Foreground(lipgloss.Color("#E8E8E8"))

	projectIndicatorStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("#6B6B6B"))

	subHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#E8E8E8"))
)

func stateStyle(state task.Status) lipgloss.Style {
	if style, ok := stateStyles[state]; ok {
		return style
	}
	return normalStyle
}

func priorityStyle(priority string) lipgloss.Style {
	if style, ok := priorityStyles[priority]; ok {
		return style
	}
	return normalStyle
}

func priorityBadge(priority string) string {
	switch priority {
	case "urgent":
		return "U"
	case "high":
		return "H"
	case "medium":
		return "M"
	case "low":
		return "L"
	default:
		return "M"
	}
}
