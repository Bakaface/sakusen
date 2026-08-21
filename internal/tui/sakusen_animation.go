package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type sakusenTickMsg time.Time

func sakusenTickCmd() tea.Cmd {
	return tea.Tick(time.Duration(tickInterval)*time.Millisecond, func(t time.Time) tea.Msg {
		return sakusenTickMsg(t)
	})
}

type planeState struct {
	x          int
	y          int
	startDelay int
}

type sakusenAnimation struct {
	planes []planeState
	width  int
	height int
	frame  int
	speed  int // columns per tick
	done   bool
}

const tickInterval = 33 // ms per tick

// newSakusenAnimation creates an animation with planes at the given screen positions.
// positions is a list of {x, y} pairs representing where each ✈ sat in the prompt view.
// durationMs is the target animation duration in milliseconds.
func newSakusenAnimation(positions [][2]int, width, height, durationMs int) sakusenAnimation {
	planes := make([]planeState, len(positions))
	for i, pos := range positions {
		planes[i] = planeState{
			x:          pos[0],
			y:          pos[1],
			startDelay: i + 2, // +2 so first rendered frame shows all planes at original positions
		}
	}

	// Compute speed: how many columns per tick to cross the screen in durationMs.
	// The last plane has the longest delay, so account for that.
	totalTicks := durationMs / tickInterval
	if totalTicks < 1 {
		totalTicks = 1
	}
	maxDelay := len(positions) + 1 // last plane's startDelay
	moveTicks := totalTicks - maxDelay
	if moveTicks < 1 {
		moveTicks = 1
	}
	speed := (width + 6) / moveTicks // +6 for trail to fully exit
	if speed < 1 {
		speed = 1
	}

	return sakusenAnimation{
		planes: planes,
		width:  width,
		height: height,
		speed:  speed,
		frame:  0,
		done:   false,
	}
}

func (a sakusenAnimation) Update() sakusenAnimation {
	a.frame++

	allDone := true
	for i := range a.planes {
		if a.frame >= a.planes[i].startDelay {
			a.planes[i].x += a.speed
		}
		if a.planes[i].x <= a.width {
			allDone = false
		}
	}

	if allDone {
		a.done = true
	}

	return a
}

func (a sakusenAnimation) View() string {
	if a.width == 0 || a.height == 0 {
		return ""
	}

	planeStyle := lipgloss.NewStyle().Foreground(promptColor)
	trailSolidStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#888888"))
	trailFadeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#555555"))

	// Build a map of row -> plane index for quick lookup
	// All planes are always visible; startDelay only gates movement
	planeByRow := make(map[int]int, len(a.planes))
	for i := range a.planes {
		planeByRow[a.planes[i].y] = i
	}

	var sb strings.Builder

	for row := 0; row < a.height; row++ {
		if row > 0 {
			sb.WriteByte('\n')
		}

		pi, hasPlane := planeByRow[row]
		if !hasPlane {
			sb.WriteString(strings.Repeat(" ", a.width))
			continue
		}

		p := a.planes[pi]

		// Trail positions (relative to plane):
		//   x-1, x-2, x-3 => solid '─'
		//   x-4, x-5, x-6 => fading '·'
		// The plane sits at p.x

		fadeStart := p.x - 6
		solidStart := p.x - 3
		planePos := p.x

		if fadeStart < 0 {
			fadeStart = 0
		}
		if solidStart < 0 {
			solidStart = 0
		}

		// Leading spaces
		if fadeStart > 0 {
			sb.WriteString(strings.Repeat(" ", fadeStart))
		}

		// Fade trail
		if solidStart > fadeStart && fadeStart < a.width {
			end := solidStart
			if end > a.width {
				end = a.width
			}
			sb.WriteString(trailFadeStyle.Render(strings.Repeat("·", end-fadeStart)))
		}

		// Solid trail
		if planePos > solidStart && solidStart < a.width {
			end := planePos
			if end > a.width {
				end = a.width
			}
			sb.WriteString(trailSolidStyle.Render(strings.Repeat("─", end-solidStart)))
		}

		// Plane character
		if planePos >= 0 && planePos < a.width {
			sb.WriteString(planeStyle.Render("✈"))
		}

		// Trailing spaces
		trailingStart := planePos + 1
		if trailingStart < 0 {
			trailingStart = 0
		}
		if trailingStart < a.width {
			sb.WriteString(strings.Repeat(" ", a.width-trailingStart))
		}
	}

	return sb.String()
}
