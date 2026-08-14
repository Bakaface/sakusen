package daemon

import (
	"encoding/json"
	"fmt"
	"time"
)

type MessageType string

const (
	MsgListAgents              MessageType = "list_agents"
	MsgListTasks               MessageType = "list_tasks"
	MsgStartAgent              MessageType = "start_agent"
	MsgStopAgent               MessageType = "stop_agent"
	MsgSubscribe               MessageType = "subscribe"
	MsgUnsubscribe             MessageType = "unsubscribe"
	MsgSendInput               MessageType = "send_input"
	MsgGetOutput               MessageType = "get_output"
	MsgGetTask                 MessageType = "get_task"
	MsgRetryTask               MessageType = "retry_task"
	MsgGetLogs                 MessageType = "get_logs"
	MsgCreateTask              MessageType = "create_task"
	MsgContinueTask            MessageType = "continue_task"
	MsgAdvanceTask             MessageType = "advance_task"
	MsgDeleteTask              MessageType = "delete_task"
	MsgUpdatePriority          MessageType = "update_priority"
	MsgUpdateField             MessageType = "update_field"
	MsgAgentList               MessageType = "agent_list"
	MsgTaskList                MessageType = "task_list"
	MsgAgentUpdate             MessageType = "agent_update"
	MsgTaskUpdate              MessageType = "task_update"
	MsgOutputChunk             MessageType = "output_chunk"
	MsgError                   MessageType = "error"
	MsgOK                      MessageType = "ok"
	MsgPing                    MessageType = "ping"
	MsgPong                    MessageType = "pong"
	MsgShutdown                MessageType = "shutdown"
	MsgTmuxActivity            MessageType = "tmux_activity"
	MsgRevertTask              MessageType = "revert_task"
	MsgUpdateDependency        MessageType = "update_dependency"
	MsgDetachBranch            MessageType = "detach_branch"
	MsgAttachBranch            MessageType = "attach_branch"
	MsgGetStepContexts         MessageType = "get_step_contexts"
	MsgUpdateStepContext       MessageType = "update_step_context"
	MsgUpdateActiveStepContext MessageType = "update_active_step_context"
	MsgGetTaskSteps            MessageType = "get_task_steps"
	MsgListWorkflows           MessageType = "list_workflows"
	MsgStopTask                MessageType = "stop_task"
	MsgCleanup                 MessageType = "cleanup"
	MsgCreateTasksAndWait      MessageType = "create_tasks_and_wait"
	MsgWaitForTasks            MessageType = "wait_for_tasks"
	MsgCreateTrack             MessageType = "create_track"
	MsgGetTrack                MessageType = "get_track"
	MsgListTracks              MessageType = "list_tracks"
	MsgSetTrackContext         MessageType = "set_track_context"         // CLI: arbitrary track by ref
	MsgUpdateTaskTrackContext  MessageType = "update_task_track_context" // MCP: own-track-only via task_id

	MsgSetTrackDescription        MessageType = "set_track_description"         // CLI: arbitrary track by ref
	MsgUpdateTaskTrackDescription MessageType = "update_task_track_description" // MCP: own-track-only via task_id
)

// IsBroadcast reports whether t is a message type the daemon pushes to
// subscribers unprompted (as opposed to a response to a client request).
// This is the single source of truth for the protocol's broadcast/response
// classification — both the daemon's broadcaster (broadcastToSubscribers)
// and client-side readers (which must route broadcasts to a separate
// channel from RPC responses) rely on it.
func IsBroadcast(t MessageType) bool {
	switch t {
	case MsgAgentUpdate, MsgTaskUpdate, MsgTmuxActivity:
		return true
	default:
		return false
	}
}

type Message struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func NewMessage(msgType MessageType, payload any) (*Message, error) {
	var raw json.RawMessage
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		raw = data
	}
	return &Message{Type: msgType, Payload: raw}, nil
}

func (m *Message) DecodePayload(v any) error {
	if m.Payload == nil {
		return nil
	}
	return json.Unmarshal(m.Payload, v)
}

type ListAgentsRequest struct{}

type StartAgentRequest struct {
	TaskID int64 `json:"task_id"`
}

type StopAgentRequest struct {
	AgentID string `json:"agent_id"`
}

type SubscribeRequest struct{}

type UnsubscribeRequest struct{}

type SendInputRequest struct {
	AgentID string `json:"agent_id"`
	Input   string `json:"input"`
}

type GetOutputRequest struct {
	AgentID  string `json:"agent_id"`
	FromLine int    `json:"from_line"`
}

type GetTaskRequest struct {
	TaskID int64 `json:"task_id"`
}

type RetryTaskRequest struct {
	TaskID int64 `json:"task_id"`
	// StepName, when non-empty, identifies the workflow step from which to
	// restart. Earlier completed steps are preserved (their contexts remain
	// available for later steps' templates). When empty, the task is restarted
	// from the beginning (step_index 0, all step records wiped).
	StepName string `json:"step_name,omitempty"`
}

type GetLogsRequest struct {
	TaskID int64 `json:"task_id"`
	Tail   int   `json:"tail"`
	Offset int   `json:"offset"` // skip first N lines (for incremental loading)
}

type ListTasksRequest struct {
	ProjectID   int64  `json:"project_id,omitempty"`   // 0 means all projects
	ProjectName string `json:"project_name,omitempty"` // filter by project name (repo basename)
}

type CreateTaskRequest struct {
	Title          string   `json:"title,omitempty"`
	Input          string   `json:"input"`
	Workflow       string   `json:"workflow,omitempty"`
	Priority       string   `json:"priority,omitempty"`
	BranchName     string   `json:"branch_name,omitempty"` // user-provided branch template
	TargetBranch   string   `json:"target_branch,omitempty"`
	CheckoutBranch string   `json:"checkout_branch,omitempty"`
	ProjectPath    string   `json:"project_path,omitempty"` // resolved to project_id by daemon
	Worktree       *bool    `json:"worktree,omitempty"`     // nil means default (true)
	BranchMode     *int     `json:"branch_mode,omitempty"`  // nil means default (0 = new branch)
	TmuxDirect     bool     `json:"tmux_direct,omitempty"`  // when true, skip workflow and go straight to tmux
	Images         []string `json:"images,omitempty"`
	BlockedBy      []int64  `json:"blocked_by,omitempty"` // task IDs that block this task
	// Track attaches the task to a track (slug or numeric ID). The sentinel
	// "none" means explicitly trackless — meaningful for create_tasks_and_wait
	// children, which otherwise inherit the parent task's track.
	Track string `json:"track,omitempty"`
}

type ContinueTaskRequest struct {
	TaskID   int64  `json:"task_id"`
	Workflow string `json:"workflow,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
}

type AdvanceTaskRequest struct {
	TaskID int64 `json:"task_id"`
}

type DeleteTaskRequest struct {
	TaskID int64 `json:"task_id"`
}

type UpdatePriorityRequest struct {
	TaskID   int64  `json:"task_id"`
	Priority string `json:"priority"`
}

type UpdateFieldRequest struct {
	TaskID int64  `json:"task_id"`
	Field  string `json:"field"`
	Value  string `json:"value"`
}

type RevertTaskRequest struct {
	TaskID int64 `json:"task_id"`
}

type UpdateDependencyRequest struct {
	TaskID    int64  `json:"task_id"`
	BlockedBy int64  `json:"blocked_by"`
	Action    string `json:"action"` // "add" or "remove"
}

type DetachBranchRequest struct {
	TaskID int64 `json:"task_id"`
}

type AttachBranchRequest struct {
	TaskID int64 `json:"task_id"`
}

type GetStepContextsRequest struct {
	TaskID int64 `json:"task_id"`
}

type GetStepContextsResponse struct {
	Steps map[string]string `json:"steps"` // step_name -> context
}

type GetTaskStepsRequest struct {
	TaskID int64 `json:"task_id"`
}

// TaskStepDetail is the per-step state returned to clients. Steps that exist
// in the workflow but have no DB row yet are included with Status == "pending".
type TaskStepDetail struct {
	Name        string     `json:"name"`
	Status      string     `json:"status"` // "pending" | "running" | "completed"
	Context     string     `json:"context,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type GetTaskStepsResponse struct {
	Steps []TaskStepDetail `json:"steps"` // ordered by workflow config
}

type UpdateStepContextRequest struct {
	TaskID   int64  `json:"task_id"`
	StepName string `json:"step_name"`
	Context  string `json:"context"`
}

// UpdateActiveStepContextRequest is the payload for MsgUpdateActiveStepContext.
// Unlike UpdateStepContextRequest (which targets a completed step from the TUI
// editor), this path enforces that step_name matches the task's currently-
// running step and supports replace/append semantics so an agent can update
// its own step context mid-session.
type UpdateActiveStepContextRequest struct {
	TaskID   int64  `json:"task_id"`
	StepName string `json:"step_name"`
	Context  string `json:"context"`
	Mode     string `json:"mode,omitempty"` // "replace" (default) or "append"
}

// ListWorkflowsRequest asks the daemon to return all workflows configured for
// a project. ProjectPath is the absolute repo-root path; the daemon resolves
// it the same way CreateTaskRequest does (GetOrCreateProject + getProjectContext).
type ListWorkflowsRequest struct {
	ProjectPath string `json:"project_path"`
}

// WorkflowStepSummary is a per-step projection returned by ListWorkflows.
// It deliberately omits prompts and timing details — the MCP surface only
// needs identifying information and execution-mode signals.
type WorkflowStepSummary struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"` // human-readable step summary (metadata)
	Mode        string `json:"mode,omitempty"`
	Agent       string `json:"agent,omitempty"` // resolved agent slug (step → workflow → default cascade)
	Tmux        bool   `json:"tmux,omitempty"`  // resolved agent runs in tmux mode
	Human       bool   `json:"human,omitempty"`
	Loop        bool   `json:"loop,omitempty"`
}

// WorkflowSummary describes a single workflow available in a project. It
// carries the pinnable New Task fields (input/worktree/branch/checkout/target)
// and FullySpec so consumers can decide skip-vs-show without re-resolving
// config. Description is human-readable metadata (NOT a pin); Input, when
// non-empty, is the pinned task input.
type WorkflowSummary struct {
	Name            string                `json:"name"`
	Description     string                `json:"description,omitempty"`        // human-readable summary (metadata)
	Input           string                `json:"input,omitempty"`              // pinned task input (hides the New Task input box)
	Worktree        *bool                 `json:"worktree,omitempty"`           // pinned worktree toggle (nil = unpinned)
	Branch          string                `json:"branch,omitempty"`             // pinned new-branch template
	Checkout        string                `json:"checkout,omitempty"`           // pinned existing branch to check out
	Target          string                `json:"target,omitempty"`             // pinned target/base branch
	FullySpec       bool                  `json:"fully_spec"`                   // every New Task field pinned → screen can be skipped
	Agent           string                `json:"agent,omitempty"`              // workflow-level explicit agent slug (steps may override)
	FirstStepIsTmux bool                  `json:"first_step_is_tmux,omitempty"` // derived; useful for picking interactive workflows
	Hidden          bool                  `json:"hidden,omitempty"`             // file-based workflow not referenced from .sortie.yml
	Source          string                `json:"source,omitempty"`             // "inline" or path to defining file
	Steps           []WorkflowStepSummary `json:"steps,omitempty"`
}

// ListWorkflowsResponse returns the flat list of workflows available in a
// project.
type ListWorkflowsResponse struct {
	ProjectPath string            `json:"project_path"`
	ProjectName string            `json:"project_name,omitempty"`
	Workflows   []WorkflowSummary `json:"workflows"`
}

type CreateTaskResponse struct {
	Task TaskInfo `json:"task"`
}

// StopTaskRequest stops a running task by stopping its agent. The daemon
// returns the post-state TaskInfo so callers can update their UI without an
// extra fetch.
type StopTaskRequest struct {
	TaskID int64 `json:"task_id"`
}

type StopTaskResponse struct {
	Task TaskInfo `json:"task"`
}

// RetryTaskResponse, RevertTaskResponse, ContinueTaskResponse,
// UpdatePriorityResponse, UpdateFieldResponse, AttachBranchResponse,
// DetachBranchResponse, UpdateDependencyResponse all return the post-mutation
// task so the TUI can update its list row in place without re-fetching.
type RetryTaskResponse struct {
	Task TaskInfo `json:"task"`
}

type RevertTaskResponse struct {
	Task TaskInfo `json:"task"`
}

type ContinueTaskResponse struct {
	Task TaskInfo `json:"task"`
}

type UpdatePriorityResponse struct {
	Task TaskInfo `json:"task"`
}

type UpdateFieldResponse struct {
	Task TaskInfo `json:"task"`
}

type AttachBranchResponse struct {
	Task TaskInfo `json:"task"`
}

type DetachBranchResponse struct {
	Task TaskInfo `json:"task"`
}

type UpdateDependencyResponse struct {
	Task TaskInfo `json:"task"`
}

// CreateTasksAndWaitRequest spawns one or more child tasks and atomically
// records task_waits_on edges from the parent so its currently-executing step
// suspends on engine return. Bundling create + wait into a single RPC makes
// the "spawn-and-suspend" pattern deterministic — the agent cannot forget to
// wait, nor wait on the wrong IDs.
type CreateTasksAndWaitRequest struct {
	// ParentTaskID is the task whose currently-running step will suspend
	// pending the children's completion. Must be a running task. Children may
	// live in any project; each child's project_path defaults to the parent's
	// project when omitted.
	ParentTaskID int64               `json:"parent_task_id"`
	Tasks        []CreateTaskRequest `json:"tasks"`
}

type CreateTasksAndWaitResponse struct {
	ParentTaskID int64      `json:"parent_task_id"`
	Children     []TaskInfo `json:"children"`
}

// WaitForTasksRequest records task_waits_on edges from ParentTaskID to each of
// the supplied child task IDs without creating any new tasks. Children may
// belong to any project — the wait relation is purely ID-based. Used to wait
// on pre-existing tasks (e.g., observe-and-block patterns).
type WaitForTasksRequest struct {
	ParentTaskID int64   `json:"parent_task_id"`
	ChildTaskIDs []int64 `json:"child_task_ids"`
}

type WaitForTasksResponse struct {
	ParentTaskID int64      `json:"parent_task_id"`
	Children     []TaskInfo `json:"children"`
}

// CreateTrackRequest creates a new track. Global tracks (project_id NULL) are
// attachable from any project; project tracks require ProjectPath.
type CreateTrackRequest struct {
	Name        string `json:"name"`
	ParentTrack string `json:"parent_track,omitempty"` // slug or numeric ID
	Global      bool   `json:"global,omitempty"`       // true = global track (project_id NULL)
	Workflow    string `json:"workflow,omitempty"`
	Context     string `json:"context,omitempty"`      // initial context seed
	Description string `json:"description,omitempty"`  // stable one-liner stating the track's purpose
	ProjectPath string `json:"project_path,omitempty"` // required unless Global
}

// TrackInfo is the wire projection of a track. Context population varies by
// endpoint: create/get carry the full own context, list_tracks clears it and
// carries ContextPreview/ContextLen instead (sizes stay visible so unbounded
// append growth is at least observable). Description, unlike Context, is
// always carried in full — it is the stable field routing agents select on.
type TrackInfo struct {
	ID             int64     `json:"id"`
	ProjectID      *int64    `json:"project_id,omitempty"`
	Global         bool      `json:"global,omitempty"`
	ParentID       *int64    `json:"parent_id,omitempty"`
	ParentSlug     string    `json:"parent_slug,omitempty"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	Description    string    `json:"description,omitempty"` // always populated (create/get/list)
	Workflow       string    `json:"workflow,omitempty"`
	Context        string    `json:"context,omitempty"`         // full own context; empty in list_tracks
	ContextPreview string    `json:"context_preview,omitempty"` // first 200 bytes (rune-aligned); list_tracks only
	ContextLen     int       `json:"context_len"`               // bytes of own context
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type CreateTrackResponse struct {
	Track TrackInfo `json:"track"`
}

type GetTrackRequest struct {
	ProjectPath string `json:"project_path,omitempty"`
	Track       string `json:"track"` // slug or numeric ID
}

type GetTrackResponse struct {
	Track           TrackInfo   `json:"track"`
	Chain           []TrackInfo `json:"chain"`            // root-first, contexts populated
	RenderedContext string      `json:"rendered_context"` // exactly what {{track.context}} yields
}

type ListTracksRequest struct {
	ProjectPath string `json:"project_path,omitempty"`
}

type ListTracksResponse struct {
	Tracks []TrackInfo `json:"tracks"`
}

// SetTrackContextRequest is the CLI-facing arbitrary-track context write
// (sortie tracks set-context). The MCP path uses UpdateTaskTrackContextRequest
// instead — same split as MsgUpdateStepContext vs MsgUpdateActiveStepContext.
type SetTrackContextRequest struct {
	ProjectPath string `json:"project_path,omitempty"`
	Track       string `json:"track"` // slug or numeric ID
	Context     string `json:"context"`
	Mode        string `json:"mode,omitempty"` // "replace" (default) or "append"
}

// UpdateTaskTrackContextRequest is the agent-facing own-track-only context
// write: the daemon resolves the task, requires it to have a track AND an
// active step, and writes only to that track (mirroring the
// update_step_context enforcement shape).
type UpdateTaskTrackContextRequest struct {
	TaskID  int64  `json:"task_id"`
	Context string `json:"context"`
	Mode    string `json:"mode,omitempty"` // "replace" (default) or "append"
}

// SetTrackDescriptionRequest is the CLI-facing arbitrary-track description
// write (sortie tracks set-description). Replace-only — there is no append
// mode, that concept belongs to the context accumulator.
type SetTrackDescriptionRequest struct {
	ProjectPath string `json:"project_path,omitempty"`
	Track       string `json:"track"` // slug or numeric ID
	Description string `json:"description"`
}

// UpdateTaskTrackDescriptionRequest is the agent-facing own-track-only
// description write, with the same enforcement shape as
// UpdateTaskTrackContextRequest: the task must have a track AND an active step.
type UpdateTaskTrackDescriptionRequest struct {
	TaskID      int64  `json:"task_id"`
	Description string `json:"description"`
}

// CleanupRequest removes worktrees, branches, and log directories for
// completed or failed tasks. A TaskID of 0 cleans up every eligible task.
type CleanupRequest struct {
	TaskID int64 `json:"task_id,omitempty"`
}

type CleanupResponse struct {
	Count int        `json:"count"`
	Tasks []TaskInfo `json:"tasks"`
}

type AgentState string

const (
	AgentPending         AgentState = "pending"
	AgentStarting        AgentState = "starting"
	AgentRunning         AgentState = "running"
	AgentWaitingForInput AgentState = "waiting_for_input"
	AgentCompleted       AgentState = "completed"
	AgentFailed          AgentState = "failed"
	AgentStopped         AgentState = "stopped"
)

type AgentInfo struct {
	ID        string     `json:"id"`
	TaskID    int64      `json:"task_id"`
	Input     string     `json:"input"`
	WorkDir   string     `json:"work_dir"`
	State     AgentState `json:"state"`
	StartedAt time.Time  `json:"started_at"`
	Error     string     `json:"error,omitempty"`
}

type ChatInfo struct {
	SessionID       string `json:"session_id"`
	TmuxSessionName string `json:"tmux_session_name,omitempty"`
	StepName        string `json:"step_name,omitempty"`
}

type TaskInfo struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	ProjectName string `json:"project_name,omitempty"`
	ProjectPath string `json:"project_path,omitempty"`
	Title       string `json:"title"`
	Input       string `json:"input"`
	Slug        string `json:"slug"`
	Workflow    string `json:"workflow,omitempty"`
	TrackID     *int64 `json:"track_id,omitempty"`
	// Track is the attached track's slug. Daemon-side enrichment (taskToInfo);
	// the DB-only fallback (TaskInfoFromTask) sets TrackID only.
	Track  string `json:"track,omitempty"`
	Status string `json:"status"`
	// EffectiveStatus is the status clients should render, mapping the
	// transport-level "tmux" status to what the workflow engine is actually
	// doing (awaiting-approval / running) based on StepHuman — see taskToInfo.
	// TaskInfoFromTask (no live daemon state available) sets it to a plain
	// copy of Status, matching what a tmux-unaware caller would show.
	EffectiveStatus  string   `json:"effective_status"`
	Priority         string   `json:"priority"`
	StepIndex        int      `json:"step_index"`
	CurrentStep      string   `json:"current_step"`
	LoopIteration    int      `json:"loop_iteration,omitempty"`
	BranchName       string   `json:"branch_name,omitempty"`
	Branch           string   `json:"branch"`
	TargetBranch     string   `json:"target_branch,omitempty"`
	CheckoutBranch   string   `json:"checkout_branch,omitempty"`
	Worktree         bool     `json:"worktree"`
	WorktreePath     string   `json:"worktree_path,omitempty"`
	WorktreeDetached bool     `json:"worktree_detached,omitempty"`
	ErrorMessage     string   `json:"error_message,omitempty"`
	Context          string   `json:"context,omitempty"`
	Images           []string `json:"images,omitempty"`
	Commits          []string `json:"commits,omitempty"`
	BlockedBy        []int64  `json:"blocked_by,omitempty"`
	// WaitsOn lists child task IDs the parent's current step is suspended
	// waiting for. Populated only while Status == "awaiting-children" — the
	// edges are cleared atomically when the parent resumes.
	WaitsOn      []int64    `json:"waits_on,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	TmuxActivity string     `json:"tmux_activity,omitempty"`
	// StepHuman reflects the Human flag on the workflow step that the task is
	// currently paused at (when Status is "tmux" or "awaiting-approval").
	// Used by the TUI to surface the "real" underlying state of a tmux task —
	// human steps render as awaiting-approval with a [wip] postfix, non-human
	// tmux steps render as running with a [T] postfix.
	StepHuman  bool      `json:"step_human,omitempty"`
	LatestChat *ChatInfo `json:"latest_chat,omitempty"`
}

type TaskListResponse struct {
	Tasks []TaskInfo `json:"tasks"`
}

type TaskUpdateResponse struct {
	Task TaskInfo `json:"task"`
}

type AgentListResponse struct {
	Agents []AgentInfo `json:"agents"`
}

type AgentUpdateResponse struct {
	Agent AgentInfo `json:"agent"`
}

type OutputChunkResponse struct {
	AgentID    string   `json:"agent_id"`
	Lines      []string `json:"lines"`
	TotalLines int      `json:"total_lines"`
}

type GetTaskResponse struct {
	Task TaskInfo `json:"task"`
}

type GetLogsResponse struct {
	TaskID     int64    `json:"task_id"`
	Lines      []string `json:"lines"`
	TotalLines int      `json:"total_lines"` // total line count before offset/tail
}

type TmuxActivityResponse struct {
	TaskID   int64  `json:"task_id"`
	Activity string `json:"activity"`
}

type ErrorResponse struct {
	Message string `json:"message"`
}

type OKResponse struct {
	Message string `json:"message,omitempty"`
}

func EncodeMessage(msg *Message) ([]byte, error) {
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func DecodeMessage(data []byte) (*Message, error) {
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("failed to decode message: %w", err)
	}
	return &msg, nil
}
