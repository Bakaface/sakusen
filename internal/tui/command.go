package tui

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Bakaface/sakusen/internal/action"
	"github.com/Bakaface/sakusen/internal/config"
	tea "github.com/charmbracelet/bubbletea"
)

// command represents a registered command that can be executed in command mode.
type command struct {
	// match returns true if the input matches this command, returning any arguments.
	match func(input string) (args string, ok bool)
	// exec executes the command with the given arguments, returning updated model and cmd.
	exec func(m Model, args string) (tea.Model, tea.Cmd)
	// help is a short description for the command.
	help string
}

// --- Declarative option registry ---
// Adding a new `:set` option requires only a new entry here.

// boolOption defines a boolean option togglable via :set name / :set noname / :set name!
type boolOption struct {
	name     string
	get      func(m *Model) bool
	set      func(m *Model, v bool)
	afterSet func(m *Model) // optional hook called after set (e.g. refilter)
}

// intOption defines a value option settable via :set name=N
type intOption struct {
	name string
	set  func(m *Model, v int)
}

// ensureAnimationConfig ensures the config animation pointer chain exists.
func ensureAnimationConfig(m *Model) {
	if m.cfg == nil {
		return
	}
	if m.cfg.Options.Animation == nil {
		m.cfg.Options.Animation = &config.AnimationConfig{}
	}
}

var boolOptions = []boolOption{
	{
		name: "number",
		get:  func(m *Model) bool { return m.list.showLineNumbers },
		set:  func(m *Model, v bool) { m.list.showLineNumbers = v },
	},
	{
		name:     "finished",
		get:      func(m *Model) bool { return m.list.showFinished },
		set:      func(m *Model, v bool) { m.list.showFinished = v },
		afterSet: func(m *Model) { m.list.applyFilter() },
	},
	{
		name: "branch",
		get:  func(m *Model) bool { return m.list.showBranch },
		set:  func(m *Model, v bool) { m.list.showBranch = v },
	},
	{
		name: "target",
		get:  func(m *Model) bool { return m.list.showTarget },
		set:  func(m *Model, v bool) { m.list.showTarget = v },
	},
	{
		name: "branchview",
		get:  func(m *Model) bool { return m.list.branchView },
		set: func(m *Model, v bool) {
			m.list.branchView = v
			if v {
				m.list.showBranch = true
			}
		},
		afterSet: func(m *Model) { m.list.applyFilter() },
	},
	{
		name: "animation",
		get:  func(m *Model) bool { return m.animationEnabled() },
		set: func(m *Model, v bool) {
			ensureAnimationConfig(m)
			if m.cfg != nil {
				m.cfg.Options.Animation.Enabled = &v
			}
		},
	},
}

var intOptions = []intOption{
	{
		name: "animation-duration",
		set: func(m *Model, v int) {
			ensureAnimationConfig(m)
			if m.cfg != nil {
				m.cfg.Options.Animation.Duration = &v
			}
		},
	},
}

// boolOptionMap and intOptionMap are built at init for O(1) lookup.
var boolOptionMap map[string]*boolOption
var intOptionMap map[string]*intOption

func init() {
	boolOptionMap = make(map[string]*boolOption, len(boolOptions))
	for i := range boolOptions {
		boolOptionMap[boolOptions[i].name] = &boolOptions[i]
	}
	intOptionMap = make(map[string]*intOption, len(intOptions))
	for i := range intOptions {
		intOptionMap[intOptions[i].name] = &intOptions[i]
	}
}

// matchSetOption is the unified matcher for all :set commands.
// It handles: "set X", "set noX", "set X!", "set X=N"
func matchSetOption(input string) (string, bool) {
	input = strings.TrimSpace(input)
	if !strings.HasPrefix(input, "set ") {
		return "", false
	}
	arg := strings.TrimSpace(input[4:])
	if arg == "" {
		return "", false
	}

	// "set X=N" — int option
	if eqIdx := strings.Index(arg, "="); eqIdx > 0 {
		name := arg[:eqIdx]
		val := arg[eqIdx+1:]
		if _, ok := intOptionMap[name]; ok {
			if _, err := strconv.Atoi(val); err == nil {
				return input, true
			}
		}
		return "", false
	}

	// "set noX" — bool disable
	if strings.HasPrefix(arg, "no") {
		name := arg[2:]
		if _, ok := boolOptionMap[name]; ok {
			return input, true
		}
		return "", false
	}

	// "set X!" — bool toggle
	if strings.HasSuffix(arg, "!") {
		name := arg[:len(arg)-1]
		if _, ok := boolOptionMap[name]; ok {
			return input, true
		}
		return "", false
	}

	// "set X" — bool enable
	if _, ok := boolOptionMap[arg]; ok {
		return input, true
	}

	return "", false
}

// execSetOption is the unified executor for all :set commands.
func execSetOption(m Model, args string) (tea.Model, tea.Cmd) {
	arg := strings.TrimSpace(args[4:]) // strip "set "

	// "set X=N"
	if eqIdx := strings.Index(arg, "="); eqIdx > 0 {
		name := arg[:eqIdx]
		val := arg[eqIdx+1:]
		if opt, ok := intOptionMap[name]; ok {
			n, _ := strconv.Atoi(val)
			if n > 0 {
				opt.set(&m, n)
			}
		}
		return m, nil
	}

	// "set noX"
	if strings.HasPrefix(arg, "no") {
		name := arg[2:]
		if opt, ok := boolOptionMap[name]; ok {
			opt.set(&m, false)
			if opt.afterSet != nil {
				opt.afterSet(&m)
			}
		}
		return m, nil
	}

	// "set X!"
	if strings.HasSuffix(arg, "!") {
		name := arg[:len(arg)-1]
		if opt, ok := boolOptionMap[name]; ok {
			opt.set(&m, !opt.get(&m))
			if opt.afterSet != nil {
				opt.afterSet(&m)
			}
		}
		return m, nil
	}

	// "set X"
	if opt, ok := boolOptionMap[arg]; ok {
		opt.set(&m, true)
		if opt.afterSet != nil {
			opt.afterSet(&m)
		}
	}
	return m, nil
}

// --- Non-set commands ---

// commands is the registry of all available commands.
var commands = []command{
	{
		match: matchQuit,
		exec:  execQuit,
		help:  "quit",
	},
	{
		match: matchLineNumber,
		exec:  execGotoLine,
		help:  "go to line number",
	},
	{
		match: matchRunTask,
		exec:  execRunTask,
		help:  "pick a workflow to start a new task (alias for :create --workflow=<name>)",
	},
	{
		match: matchActionVerb,
		exec:  execActionVerb,
		help:  "run an action verb (see internal/action — :stop, :retry, :revert, ...)",
	},
	{
		match: matchSetOption,
		exec:  execSetOption,
		help:  "set option (boolean or value)",
	},
	{
		match: matchNoh,
		exec:  execNoh,
		help:  "clear search highlights",
	},
}

// matchActionVerb matches any registered action ID followed by an optional
// space-delimited argument string. The kebab-case ID is treated literally
// so callers cannot accidentally hit a verb with a typoed prefix.
func matchActionVerb(input string) (string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", false
	}
	for id := range action.Registry {
		if input == id {
			return "", true
		}
		prefix := id + " "
		if strings.HasPrefix(input, prefix) {
			return strings.TrimSpace(input[len(prefix):]), true
		}
	}
	return "", false
}

// execActionVerb dispatches the matched palette command to the action
// registry. Parse converts the raw argument tail into the verb's typed Args;
// Run executes via the shared TUI Ctx. The Result.Message is surfaced as a
// status banner so the user sees feedback even though the TUI's Out is
// io.Discard.
func execActionVerb(m Model, rawArgs string) (tea.Model, tea.Cmd) {
	// Re-resolve the original input by finding the verb prefix again.
	verb, args := splitActionInput(m.commandInput, rawArgs)
	if verb == "" {
		m.err = fmt.Errorf("unknown action")
		return m, nil
	}
	entry, ok := action.Registry[verb]
	if !ok {
		m.err = fmt.Errorf("unknown action: %s", verb)
		return m, nil
	}
	if m.client == nil && verb != "validate" {
		m.err = fmt.Errorf("not connected to daemon")
		return m, nil
	}
	parsed, err := entry.Parse(args)
	if err != nil {
		m.err = err
		return m, nil
	}
	if v, ok := parsed.(action.Args); ok {
		if err := v.Validate(); err != nil {
			m.err = err
			return m, nil
		}
	}

	return m, func() tea.Msg {
		ctx := action.Ctx{Cfg: m.cfg, Client: m.client, Out: io.Discard}
		res, err := entry.Run(ctx, parsed)
		if err != nil {
			return errorMsg(err)
		}
		return paletteActionMsg{verb: verb, result: res}
	}
}

// splitActionInput finds the verb prefix of the palette input so the
// dispatcher does not have to look it up a second time inside the closure.
// rawArgs is already the post-verb tail (or empty if no args were supplied).
func splitActionInput(input, rawArgs string) (string, string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", ""
	}
	if _, ok := action.Registry[input]; ok {
		return input, ""
	}
	for id := range action.Registry {
		prefix := id + " "
		if strings.HasPrefix(input, prefix) {
			return id, rawArgs
		}
	}
	return "", rawArgs
}

// paletteActionMsg carries the result of an action invoked through the
// command palette. The Update reducer applies any Task / Tasks updates to
// the list and surfaces Result.Message as a status banner.
type paletteActionMsg struct {
	verb   string
	result action.Result
}

// quitCommands lists all vim-style quit commands that close the TUI.
var quitCommands = map[string]bool{
	"q": true, "q!": true,
	"qa": true, "qa!": true,
	"qall": true, "qall!": true,
	"wq": true, "wq!": true,
	"wqa": true, "wqa!": true,
	"x": true, "x!": true,
	"xa": true, "xa!": true,
	"xall": true, "xall!": true,
}

// matchQuit matches vim-style quit commands (q, q!, qa, wq, x, etc.).
func matchQuit(input string) (string, bool) {
	input = strings.TrimSpace(input)
	if quitCommands[input] {
		return input, true
	}
	return "", false
}

// execQuit closes the TUI, mirroring the behavior of the "q" keybinding.
func execQuit(m Model, _ string) (tea.Model, tea.Cmd) {
	m.quitting = true
	if m.client != nil {
		m.client.Close()
	}
	return m, tea.Quit
}

// matchLineNumber matches a bare positive number input (e.g. "3", "12").
func matchLineNumber(input string) (string, bool) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", false
	}
	if n, err := strconv.Atoi(input); err == nil && n > 0 {
		return input, true
	}
	return "", false
}

// execGotoLine jumps the cursor to the given 1-based line number.
func execGotoLine(m Model, args string) (tea.Model, tea.Cmd) {
	n, _ := strconv.Atoi(args)
	// Convert from 1-based (displayed) to 0-based (internal)
	m.list.GotoIndex(n - 1)
	return m, nil
}

// matchNoh matches the "noh" or "nohlsearch" commands.
func matchNoh(input string) (string, bool) {
	input = strings.TrimSpace(input)
	if input == "noh" || input == "nohlsearch" {
		return input, true
	}
	return "", false
}

// execNoh clears search highlights.
func execNoh(m Model, _ string) (tea.Model, tea.Cmd) {
	m.list.matchedIndices = nil
	m.list.currentMatchIdx = 0
	m.searchQuery = ""
	return m, nil
}

// matchRunTask matches "RunTask" / "RunTask <name>" commands.
func matchRunTask(input string) (string, bool) {
	return matchRunCommand("RunTask", input)
}

// matchRunCommand is the shared matcher: accepts the bare name and "name arg".
func matchRunCommand(name, input string) (string, bool) {
	input = strings.TrimSpace(input)
	if input == name {
		return "", true
	}
	if strings.HasPrefix(input, name+" ") {
		return strings.TrimSpace(input[len(name)+1:]), true
	}
	return "", false
}

// execRunTask opens a fuzzy picker over all workflows. On select (or when a
// name is passed as argument), launches the workflow: fully-pinned workflows
// create a task immediately, otherwise the new-task prompt opens with the
// workflow preselected and pinned fields populated.
func execRunTask(m Model, args string) (tea.Model, tea.Cmd) {
	if m.client == nil || m.projectPath == "" {
		m.err = fmt.Errorf("not connected to daemon")
		return m, nil
	}
	if m.cfg == nil {
		m.err = fmt.Errorf("no config loaded")
		return m, nil
	}

	allNames := m.cfg.ListAllWorkflowNames()
	if args != "" {
		// Direct invocation with workflow name.
		if !contains(allNames, args) {
			m.err = fmt.Errorf("unknown workflow: %s", args)
			return m, nil
		}
		return m.launchWorkflow(args)
	}

	if len(allNames) == 0 {
		m.err = fmt.Errorf("no workflows configured")
		return m, nil
	}
	descs := make([]string, len(allNames))
	for i, name := range allNames {
		if wf := m.cfg.GetTaskWorkflow(name); wf != nil {
			descs[i] = wf.Description
		}
	}
	m.selector = selector{
		kind:            selectorWorkflow,
		title:           "Run Task",
		items:           append([]string(nil), allNames...),
		descriptions:    append([]string(nil), descs...),
		filterable:      true,
		allItems:        allNames,
		allDescriptions: descs,
	}
	return m, nil
}

// contains reports whether names contains s.
func contains(names []string, s string) bool {
	for _, n := range names {
		if n == s {
			return true
		}
	}
	return false
}

// completeRunTask returns tab-completed command input for RunTask. It matches
// workflow names (including hidden) against the partial input after the command.
func completeRunTask(m Model, input string) (string, bool) {
	if m.cfg == nil {
		return "", false
	}
	return completeRunCommand(input, "RunTask", m.cfg.ListAllWorkflowNames())
}

// completeRunCommand handles tab-completion for a single "RunFoo [name]" command.
func completeRunCommand(input, name string, allNames []string) (string, bool) {
	// "RunFo" → complete to "RunFoo "
	if input == name {
		return name + " ", true
	}
	if !strings.HasPrefix(input, name+" ") {
		return "", false
	}
	partial := input[len(name)+1:]
	partialLower := strings.ToLower(partial)
	var matches []string
	for _, n := range allNames {
		if strings.HasPrefix(strings.ToLower(n), partialLower) {
			matches = append(matches, n)
		}
	}
	if len(matches) == 1 {
		return name + " " + matches[0], true
	}
	if len(matches) > 1 {
		prefix := matches[0]
		for _, m := range matches[1:] {
			prefix = commonPrefix(prefix, m)
		}
		if len(prefix) > len(partial) {
			return name + " " + prefix, true
		}
	}
	return "", false
}

// commonPrefix returns the longest common prefix of two strings (case-preserving).
func commonPrefix(a, b string) string {
	minLen := min(len(a), len(b))
	for i := 0; i < minLen; i++ {
		if !strings.EqualFold(string(a[i]), string(b[i])) {
			return a[:i]
		}
	}
	return a[:minLen]
}

// executeCommand finds and runs the first matching command for the given input.
func executeCommand(m Model, input string) (tea.Model, tea.Cmd) {
	input = strings.TrimSpace(input)
	if input == "" {
		return m, nil
	}
	for _, cmd := range commands {
		if args, ok := cmd.match(input); ok {
			return cmd.exec(m, args)
		}
	}
	m.err = fmt.Errorf("unknown command: %s", input)
	return m, nil
}
