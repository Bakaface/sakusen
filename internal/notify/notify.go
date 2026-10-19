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
	return NewWithBackend(cfg, selectBackend())
}

// NewWithBackend builds a Notifier that delivers through backend instead of
// the OS default.
func NewWithBackend(cfg *config.NotificationsConfig, backend Backend) *Notifier {
	return &Notifier{cfg: cfg, backend: backend}
}

// Config returns the global notification switches, the base tier that
// per-task overrides are resolved against.
func (n *Notifier) Config() config.NotificationsConfig {
	return *n.cfg
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

// AgentCompleted, AgentFailed and AgentWaitingForInput send only when on is
// true; callers pass the task's resolved per-event switch. Send still applies
// the global Enabled gate.
func (n *Notifier) AgentCompleted(taskID, description string, on bool) error {
	if !on {
		return nil
	}

	return n.Send(
		"Agent Completed",
		taskID+": "+description,
		UrgencyNormal,
	)
}

func (n *Notifier) AgentFailed(taskID, description, errMsg string, on bool) error {
	if !on {
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

func (n *Notifier) AgentWaitingForInput(taskID, description string, on bool) error {
	if !on {
		return nil
	}

	return n.Send(
		"Agent Needs Input",
		taskID+": "+description,
		UrgencyCritical,
	)
}

// maxNotifiedPaths caps how many blocking paths a merge-blocked notification
// lists.
const maxNotifiedPaths = 3

func (n *Notifier) MergeBlocked(taskID int64, title string, paths []string) error {
	if !n.cfg.OnMergeBlocked {
		return nil
	}

	body := fmt.Sprintf("#%d: %s\nTarget checkout has uncommitted changes:", taskID, title)
	for i, p := range paths {
		if i == maxNotifiedPaths {
			body += fmt.Sprintf("\n…and %d more", len(paths)-maxNotifiedPaths)
			break
		}
		body += "\n" + p
	}

	return n.Send(
		"Merge Blocked",
		body,
		UrgencyNormal,
	)
}
