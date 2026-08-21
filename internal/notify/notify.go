package notify

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/Bakaface/sakusen/internal/config"
)

type Notifier struct {
	cfg     *config.NotificationsConfig
	backend Backend
}

func New(cfg *config.NotificationsConfig) *Notifier {
	return &Notifier{cfg: cfg, backend: selectBackend()}
}

type Urgency string

const (
	UrgencyLow      Urgency = "low"
	UrgencyNormal   Urgency = "normal"
	UrgencyCritical Urgency = "critical"
)

// Backend delivers a single desktop notification. Implementations are
// OS-specific; selectBackend picks the right one for runtime.GOOS.
type Backend interface {
	Notify(title, body string, urgency Urgency) error
}

// selectBackend returns the Backend appropriate for the current OS.
// notify-send is Linux-only (most desktop environments ship it); macOS has
// no notify-send but can post notifications via osascript.
func selectBackend() Backend {
	switch runtime.GOOS {
	case "darwin":
		return osascriptBackend{}
	default:
		return notifySendBackend{}
	}
}

// notifySendBackend shells out to the `notify-send` CLI (Linux).
type notifySendBackend struct{}

func (notifySendBackend) Notify(title, body string, urgency Urgency) error {
	args := []string{
		"--app-name=Sakusen",
		"--urgency=" + string(urgency),
		title,
		body,
	}
	return exec.Command("notify-send", args...).Run()
}

// osascriptBackend shells out to `osascript` to post a notification via
// `display notification` (macOS). AppleScript's display notification has no
// urgency concept, so urgency is accepted for interface parity but unused.
type osascriptBackend struct{}

func (osascriptBackend) Notify(title, body string, _ Urgency) error {
	script := fmt.Sprintf(
		"display notification %s with title %s",
		appleScriptString(body),
		appleScriptString(title),
	)
	return exec.Command("osascript", "-e", script).Run()
}

// appleScriptString escapes and quotes s for safe interpolation into an
// AppleScript string literal.
func appleScriptString(s string) string {
	escaped := strings.ReplaceAll(s, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func (n *Notifier) Send(title, body string, urgency Urgency) error {
	if !n.cfg.Enabled {
		return nil
	}

	return n.backend.Notify(title, body, urgency)
}

func (n *Notifier) AgentCompleted(taskID, description string) error {
	if !n.cfg.OnComplete {
		return nil
	}

	return n.Send(
		"Agent Completed",
		taskID+": "+description,
		UrgencyNormal,
	)
}

func (n *Notifier) AgentFailed(taskID, description, errMsg string) error {
	if !n.cfg.OnFailed {
		return nil
	}

	body := taskID + ": " + description
	if errMsg != "" {
		body += "\nError: " + errMsg
	}

	return n.Send(
		"Agent Failed",
		body,
		UrgencyCritical,
	)
}

func (n *Notifier) AllTasksCompleted() error {
	if !n.cfg.OnComplete {
		return nil
	}

	return n.Send(
		"All Tasks Completed",
		"All tasks have finished processing.",
		UrgencyNormal,
	)
}

func (n *Notifier) AgentWaitingForInput(taskID, description string) error {
	if !n.cfg.OnWaitingInput {
		return nil
	}

	return n.Send(
		"Agent Needs Input",
		taskID+": "+description,
		UrgencyCritical,
	)
}
