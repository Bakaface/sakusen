package daemon

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"path/filepath"

	"github.com/Bakaface/sakusen/internal/agent"
	"github.com/Bakaface/sakusen/internal/config"
	"github.com/Bakaface/sakusen/internal/db"
	gitpkg "github.com/Bakaface/sakusen/internal/git"
	"github.com/Bakaface/sakusen/internal/merge"
	"github.com/Bakaface/sakusen/internal/notify"
	"github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/tmux"
	"github.com/Bakaface/sakusen/internal/workflow"
)

const (
	// agentShutdownGracePeriod is the time allowed for agents to finish before force-stopping on shutdown.
	agentShutdownGracePeriod = 30 * time.Second

	// scannerBufferSize is the max message size accepted on the Unix socket (10MB).
	scannerBufferSize = 10 * 1024 * 1024
)

type projectContext struct {
	cfg           *config.Config
	engine        *workflow.Engine
	repoRoot      string
	repo          *gitpkg.Repo // git operations scoped to repoRoot / task worktrees
	configModTime time.Time    // zero = no .sakusen.yml at load time
	// tracksFingerprint is a cheap freshness signal over the project's
	// .sakusen/tracks tree and the global ~/.sakusen/tracks tree
	// ("maxMtimeUnixNano:fileCount", "" when neither exists). Without it,
	// track workflow files created at runtime would never be picked up —
	// the config cache invalidates only on .sakusen.yml mtime otherwise.
	tracksFingerprint string
}

type Server struct {
	cfg      *config.Config
	listener net.Listener
	manager  *agent.Manager
	database taskStore
	notifier *notify.Notifier

	projectsMu sync.RWMutex
	projects   map[int64]*projectContext

	// mergeLocks hands out per-repo merge serializers. Owned here so the lock
	// survives engine reconstruction (the merge invariant is per-repo, not
	// per-engine).
	mergeLocks *merge.Locks

	mu           sync.RWMutex
	clients      map[net.Conn]bool
	subscribers  map[net.Conn]bool
	tmuxActivity map[int64]string

	// tmuxAutoState tracks per-task auto-advance bookkeeping for tasks
	// in StatusTmux. Lives behind mu. Entries are populated as the tmux
	// monitor observes activity / sentinels and cleared when the task
	// leaves StatusTmux (the daemon kills the session, or the user
	// finalizes the task manually).
	tmuxAutoState map[int64]*tmuxAutoEntry

	// enginePaused records tasks the workflow engine explicitly paused at an
	// approval gate during their current agent run (set via the engine's pause
	// callback, wired in getProjectContext). Lives behind mu. Consumed by
	// onAgentStateChange when the agent completes: a pause-looking DB status
	// WITHOUT this signal means the status was overwritten by a concurrent
	// request while the agent ran, and finalization must proceed rather than
	// stranding the task. Kept on the Server (not the Engine) because engines
	// are reconstructed on .sakusen.yml changes.
	enginePaused map[int64]bool

	// taskFlowLocks serializes user/daemon-driven lifecycle transitions
	// (advance, continue-from-pause) per task. Guarded by taskFlowMu. Without
	// it, two concurrent requests can both observe StatusTmux and race into
	// StartAgent; the loser's error path then corrupts the task status (the
	// 2026-07-13 task #220 incident).
	taskFlowMu    sync.Mutex
	taskFlowLocks map[int64]*sync.Mutex

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	shutdownOnce sync.Once

	// shutdownDone is closed as the last statement of shutdown(), after the
	// database is closed and the pid/socket files are removed. Start() blocks
	// on it after wg.Wait() so process exit cannot preempt shutdown's tail:
	// shutdown() runs on a connection (or signal) goroutine and its first act —
	// closing the listener — unblocks the accept loop, so wg.Wait() returns
	// while shutdown() is still mid-flight (the sakusen#337 stale-pid race).
	shutdownDone chan struct{}
}

// tmuxAutoEntry holds the daemon's per-task state for tmux auto-advance.
//
// advancing is set true the moment the daemon decides to advance the
// task, so re-entrant ticks of the monitor loop don't fire StartAgent
// twice while ResumeAfterApproval is still spinning up. Cleared when
// the task leaves StatusTmux.
type tmuxAutoEntry struct {
	advancing bool
}

func NewServer(cfg *config.Config, database taskStore) *Server {
	ctx, cancel := context.WithCancel(context.Background())
	notifier := notify.New(&cfg.Notifications)
	return &Server{
		cfg:           cfg,
		database:      database,
		manager:       agent.NewManager(cfg.MaxWorkers, config.DefaultOutputBufferLines),
		notifier:      notifier,
		projects:      make(map[int64]*projectContext),
		mergeLocks:    merge.NewLocks(),
		clients:       make(map[net.Conn]bool),
		subscribers:   make(map[net.Conn]bool),
		tmuxActivity:  make(map[int64]string),
		tmuxAutoState: make(map[int64]*tmuxAutoEntry),
		enginePaused:  make(map[int64]bool),
		taskFlowLocks: make(map[int64]*sync.Mutex),
		ctx:           ctx,
		cancel:        cancel,
		shutdownDone:  make(chan struct{}),
	}
}

// markEnginePaused is the Engine pause callback (see Server.enginePaused).
// Runs synchronously on the runner goroutine right before the engine writes
// the pause status, so by the time the agent completes the signal is in place.
func (s *Server) markEnginePaused(taskID int64) {
	s.mu.Lock()
	s.enginePaused[taskID] = true
	s.mu.Unlock()
}

// consumeEnginePaused reports whether the engine paused this task during its
// current agent run, clearing the signal so it can't leak into a later run.
func (s *Server) consumeEnginePaused(taskID int64) bool {
	s.mu.Lock()
	paused := s.enginePaused[taskID]
	delete(s.enginePaused, taskID)
	s.mu.Unlock()
	return paused
}

// taskFlowLock returns the per-task mutex serializing lifecycle transitions
// (see Server.taskFlowLocks). Locks are created on demand and never removed:
// one mutex per task ever touched is negligible, and removal would reintroduce
// the very race this exists to close.
func (s *Server) taskFlowLock(taskID int64) *sync.Mutex {
	s.taskFlowMu.Lock()
	defer s.taskFlowMu.Unlock()
	mu, ok := s.taskFlowLocks[taskID]
	if !ok {
		mu = &sync.Mutex{}
		s.taskFlowLocks[taskID] = mu
	}
	return mu
}

// getProjectContext lazy-loads and caches per-project config (.sakusen.yml) and
// the workflow Engine bound to it. Callers MUST route through this function —
// every site in internal/daemon/ does, and the only direct config.LoadForProject
// call lives below.
//
// Design note (audited 2026-05, sakusen#62): this is a private method, not a
// ProjectContextStore module. The two direct reads of s.projects elsewhere are
// intentional and would survive extraction — broadcast.go's taskToInfo() peeks
// the map without triggering a load (a serializer should not issue DB queries),
// and shutdown() iterates the live cache to kill tmux sessions. There is no
// second adapter today (tests prime the real cache via this method), so
// promoting the seam to an exported module + interface would be one-adapter
// indirection without leverage. The natural cache key is projectID (carried on
// every *task.Task), not repoRoot, so the conventional Store.Get(repoRoot)
// shape would push an extra DB lookup into callers. Invalidation lives below
// via .sakusen.yml mod-time check; no handler needs an Invalidate() API.
// Concurrent first-time loads can race, but the duplicated work is a YAML parse
// and the loser's *projectContext is GC'd — per-repo merge locks (now owned by
// internal/merge) are keyed independently, so merge serialization survives the
// race. Revisit only if a second adapter (in-memory fake, alternate backing
// store, cross-process cache) appears.
func (s *Server) getProjectContext(projectID int64) (*projectContext, error) {
	s.projectsMu.RLock()
	if pc, ok := s.projects[projectID]; ok {
		s.projectsMu.RUnlock()

		// Check if .sakusen.yml has changed since we cached
		configPath := filepath.Join(pc.repoRoot, ".sakusen.yml")
		info, statErr := os.Stat(configPath)
		fresh := false
		switch {
		case statErr == nil && !pc.configModTime.IsZero():
			fresh = info.ModTime().Equal(pc.configModTime)
		case statErr != nil && pc.configModTime.IsZero():
			fresh = true // was absent, still absent
		}
		// Second freshness signal: the track-workflow trees. Either signal
		// differing evicts and reloads.
		if fresh && tracksFingerprint(pc.repoRoot) != pc.tracksFingerprint {
			fresh = false
		}
		if fresh {
			return pc, nil
		}
		// Config changed — evict and fall through to reload
		s.projectsMu.Lock()
		delete(s.projects, projectID)
		s.projectsMu.Unlock()
		log.Printf("Config changed for project %d (%s), reloading", projectID, pc.repoRoot)
	} else {
		s.projectsMu.RUnlock()
	}

	proj, err := s.database.GetProject(projectID)
	if err != nil {
		return nil, fmt.Errorf("failed to get project: %w", err)
	}

	projCfg, err := config.LoadForProject(proj.Path)
	if err != nil {
		log.Printf("Warning: failed to load config for project %s, using defaults: %v", proj.Path, err)
		projCfg = s.cfg
	}

	// Stat config file for future invalidation checks
	var modTime time.Time
	configPath := filepath.Join(proj.Path, ".sakusen.yml")
	if info, err := os.Stat(configPath); err == nil {
		modTime = info.ModTime()
	}

	engine := workflow.NewEngine(projCfg, s.database, s.notifier, proj.Path, s.mergeLocks.For(proj.Path))
	// Wire the explicit pause signal so onAgentStateChange can distinguish a
	// genuine engine pause from a pause-looking status left behind by a
	// concurrent request. Re-wired here on every engine (re)construction so
	// config reloads don't drop the signal path.
	engine.SetPauseCallback(s.markEnginePaused)

	pc := &projectContext{
		cfg:               projCfg,
		engine:            engine,
		repoRoot:          proj.Path,
		repo:              gitpkg.NewRepo(proj.Path),
		configModTime:     modTime,
		tracksFingerprint: tracksFingerprint(proj.Path),
	}

	s.projectsMu.Lock()
	s.projects[projectID] = pc
	s.projectsMu.Unlock()

	return pc, nil
}

// tracksFingerprint returns "maxMtimeUnixNano:fileCount" over the project's
// .sakusen/tracks tree and the global ~/.sakusen/tracks tree, or "" when neither
// exists / contains anything. The walk is shallow and deterministic: each
// tracks dir, each slug-dir entry, each <slug>/workflows dir, and each
// .yml/.yaml file inside it contribute their mtimes; only workflow files count
// toward fileCount (so deletions are caught by the count component even when
// max-mtime doesn't move). Used by getProjectContext as a second config-cache
// freshness signal alongside the .sakusen.yml mtime.
func tracksFingerprint(repoRoot string) string {
	var maxMtime int64
	var fileCount int

	note := func(t time.Time) {
		if n := t.UnixNano(); n > maxMtime {
			maxMtime = n
		}
	}

	scanTracksDir := func(dir string) {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			return
		}
		note(info.ModTime())
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			wfDir := filepath.Join(dir, entry.Name(), "workflows")
			wfInfo, err := os.Stat(wfDir)
			if err != nil || !wfInfo.IsDir() {
				continue
			}
			note(wfInfo.ModTime())
			files, err := os.ReadDir(wfDir)
			if err != nil {
				continue
			}
			for _, f := range files {
				ext := filepath.Ext(f.Name())
				if ext != ".yml" && ext != ".yaml" {
					continue
				}
				if fi, err := f.Info(); err == nil {
					note(fi.ModTime())
				}
				fileCount++
			}
		}
	}

	if repoRoot != "" {
		scanTracksDir(filepath.Join(repoRoot, ".sakusen", "tracks"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		scanTracksDir(filepath.Join(home, ".sakusen", "tracks"))
	}

	if maxMtime == 0 && fileCount == 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d", maxMtime, fileCount)
}

// projectLogPrefix returns a "[projectname] " prefix for log messages.
// If the project name cannot be resolved, it returns an empty string.
func (s *Server) projectLogPrefix(projectID int64) string {
	if pc, err := s.getProjectContext(projectID); err == nil && pc.cfg.Project.Name != "" {
		return "[" + pc.cfg.Project.Name + "] "
	}
	if proj, err := s.database.GetProject(projectID); err == nil && proj.Name != "" {
		return "[" + proj.Name + "] "
	}
	return ""
}

func (s *Server) getProjectDataDir(t *task.Task) string {
	pc, err := s.getProjectContext(t.ProjectID)
	if err != nil {
		return config.GetGlobalDataDir()
	}
	return filepath.Join(pc.repoRoot, ".sakusen")
}

func (s *Server) getProjectRepoRoot(t *task.Task) string {
	pc, err := s.getProjectContext(t.ProjectID)
	if err != nil {
		return ""
	}
	return pc.repoRoot
}

func (s *Server) Start() error {
	if err := s.cfg.EnsureDirs(); err != nil {
		return fmt.Errorf("failed to create directories: %w", err)
	}

	if err := s.writePidFile(); err != nil {
		return fmt.Errorf("failed to write pid file: %w", err)
	}

	if err := os.RemoveAll(s.cfg.SocketPath); err != nil {
		return fmt.Errorf("failed to remove old socket: %w", err)
	}

	listener, err := net.Listen("unix", s.cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("failed to listen on socket: %w", err)
	}
	s.listener = listener

	s.manager.SetStateChangeCallback(s.onAgentStateChange)

	if err := s.recoverOrphanedTasks(); err != nil {
		log.Printf("Warning: failed to recover orphaned tasks: %v", err)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Printf("Received signal %q (pid=%d, ppid=%d)", sig, os.Getpid(), os.Getppid())
		s.Shutdown()
	}()

	s.wg.Add(1)
	go s.acceptLoop()

	s.wg.Add(1)
	go s.taskPollerLoop()

	s.wg.Add(1)
	go s.tmuxMonitorLoop()

	log.Printf("Daemon started, listening on %s (pid=%d, ppid=%d)", s.cfg.SocketPath, os.Getpid(), os.Getppid())

	s.wg.Wait()

	// The loops above all exit only because shutdown() closed the listener /
	// canceled the context, so a shutdown is guaranteed to be in flight here.
	// Wait for its tail (DB close, pid/socket removal, "Daemon stopped" log)
	// to finish before returning and letting the process exit.
	<-s.shutdownDone

	return nil
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			select {
			case <-s.ctx.Done():
				return
			default:
				log.Printf("Accept error: %v", err)
				continue
			}
		}

		s.mu.Lock()
		s.clients[conn] = true
		s.mu.Unlock()

		go s.handleConnection(conn)
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	log.Printf("daemon: connection accepted (conn=%p)", conn)
	defer func() {
		s.mu.Lock()
		delete(s.clients, conn)
		delete(s.subscribers, conn)
		s.mu.Unlock()
		conn.Close()
	}()

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 0, 1024*1024), scannerBufferSize)
	for scanner.Scan() {
		msg, err := DecodeMessage(scanner.Bytes())
		if err != nil {
			s.sendError(conn, "invalid message format")
			continue
		}

		s.handleMessage(conn, msg)
	}

	// Connection-lifecycle logging — without this, broken-pipe symptoms are
	// impossible to root-cause from post-hoc state (see sakusen-102).
	if err := scanner.Err(); err != nil {
		log.Printf("daemon: connection closed with scanner error (conn=%p): %v", conn, err)
	} else {
		log.Printf("daemon: connection closed (EOF) (conn=%p)", conn)
	}
}

func (s *Server) handleMessage(conn net.Conn, msg *Message) {
	switch msg.Type {
	case MsgPing:
		s.sendMessage(conn, MsgPong, nil)

	case MsgListAgents:
		s.handleListAgents(conn)

	case MsgListTasks:
		var req ListTasksRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleListTasks(conn, req)

	case MsgStartAgent:
		var req StartAgentRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleStartAgent(conn, req)

	case MsgStopAgent:
		var req StopAgentRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleStopAgent(conn, req)

	case MsgSubscribe:
		s.handleSubscribe(conn)

	case MsgUnsubscribe:
		s.handleUnsubscribe(conn)

	case MsgGetOutput:
		var req GetOutputRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleGetOutput(conn, req)

	case MsgGetTask:
		var req GetTaskRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleGetTask(conn, req)

	case MsgRetryTask:
		var req RetryTaskRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleRetryTask(conn, req)

	case MsgGetLogs:
		var req GetLogsRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleGetLogs(conn, req)

	case MsgCreateTask:
		var req CreateTaskRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleCreateTask(conn, req)

	case MsgContinueTask:
		var req ContinueTaskRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleContinueTask(conn, req)

	case MsgAdvanceTask:
		var req AdvanceTaskRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleAdvanceTask(conn, req)

	case MsgDeleteTask:
		var req DeleteTaskRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleDeleteTask(conn, req)

	case MsgUpdatePriority:
		var req UpdatePriorityRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleUpdatePriority(conn, req)

	case MsgUpdateField:
		var req UpdateFieldRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleUpdateField(conn, req)

	case MsgRevertTask:
		var req RevertTaskRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleRevertTask(conn, req)

	case MsgUpdateDependency:
		var req UpdateDependencyRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleUpdateDependency(conn, req)

	case MsgDetachBranch:
		var req DetachBranchRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleDetachBranch(conn, req)

	case MsgAttachBranch:
		var req AttachBranchRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleAttachBranch(conn, req)

	case MsgGetStepContexts:
		var req GetStepContextsRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleGetStepContexts(conn, req)

	case MsgGetTaskSteps:
		var req GetTaskStepsRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleGetTaskSteps(conn, req)

	case MsgUpdateStepContext:
		var req UpdateStepContextRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleUpdateStepContext(conn, req)

	case MsgUpdateActiveStepContext:
		var req UpdateActiveStepContextRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleUpdateActiveStepContext(conn, req)

	case MsgListWorkflows:
		var req ListWorkflowsRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleListWorkflows(conn, req)

	case MsgStopTask:
		var req StopTaskRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleStopTask(conn, req)

	case MsgCleanup:
		var req CleanupRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleCleanup(conn, req)

	case MsgCreateTasksAndWait:
		var req CreateTasksAndWaitRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleCreateTasksAndWait(conn, req)

	case MsgWaitForTasks:
		var req WaitForTasksRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleWaitForTasks(conn, req)

	case MsgCreateTrack:
		var req CreateTrackRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleCreateTrack(conn, req)

	case MsgGetTrack:
		var req GetTrackRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleGetTrack(conn, req)

	case MsgListTracks:
		var req ListTracksRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleListTracks(conn, req)

	case MsgSetTrackContext:
		var req SetTrackContextRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleSetTrackContext(conn, req)

	case MsgUpdateTaskTrackContext:
		var req UpdateTaskTrackContextRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleUpdateTaskTrackContext(conn, req)

	case MsgSetTrackDescription:
		var req SetTrackDescriptionRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleSetTrackDescription(conn, req)

	case MsgUpdateTaskTrackDescription:
		var req UpdateTaskTrackDescriptionRequest
		if err := msg.DecodePayload(&req); err != nil {
			s.sendError(conn, "invalid payload")
			return
		}
		s.handleUpdateTaskTrackDescription(conn, req)

	case MsgShutdown:
		s.mu.RLock()
		clientCount := len(s.clients)
		s.mu.RUnlock()
		log.Printf("Received MsgShutdown from socket client (conn=%p, total_clients=%d)", conn, clientCount)
		s.Shutdown()

	default:
		s.sendError(conn, "unknown message type")
	}
}

func (s *Server) Shutdown() {
	s.shutdownOnce.Do(s.shutdown)
}

func (s *Server) shutdown() {
	buf := make([]byte, 8192)
	n := runtime.Stack(buf, false)
	log.Printf("Shutting down daemon (pid=%d, ppid=%d)\nshutdown caller goroutine:\n%s", os.Getpid(), os.Getppid(), buf[:n])

	if s.listener != nil {
		s.listener.Close()
	}

	if s.database != nil {
		runningTasks, err := s.database.GetRunningTasks()
		if err == nil {
			// Collect repo roots that may have in-progress merges
			dirtyRepoRoots := make(map[string]bool)
			for _, t := range runningTasks {
				log.Printf("%sMarking task #%d as failed (daemon shutdown)", s.projectLogPrefix(t.ProjectID), t.ID)
				if err := s.database.UpdateTaskError(t.ID, "daemon shutdown"); err != nil {
					log.Printf("%sFailed to mark task #%d as failed: %v", s.projectLogPrefix(t.ProjectID), t.ID, err)
				}
				if repoRoot := s.getProjectRepoRoot(t); repoRoot != "" {
					dirtyRepoRoots[repoRoot] = true
				}
			}
			// Clean up any staged changes left by interrupted merges
			for repoRoot := range dirtyRepoRoots {
				if err := gitpkg.NewRepo(repoRoot).CleanRepoState(); err != nil {
					log.Printf("Warning: failed to clean repo state for %s on shutdown: %v", repoRoot, err)
				} else {
					log.Printf("Cleaned repo state for %s on shutdown", repoRoot)
				}
			}
		}
	}

	s.manager.Shutdown(agentShutdownGracePeriod)

	s.projectsMu.RLock()
	for _, pc := range s.projects {
		if sessions, err := tmux.ListSessions(tmux.SessionPrefix(pc.cfg.Project.Name)); err == nil {
			for _, sess := range sessions {
				sess.Kill()
			}
		}
	}
	s.projectsMu.RUnlock()

	s.cancel()

	s.mu.Lock()
	for conn := range s.clients {
		conn.Close()
	}
	s.mu.Unlock()

	if s.database != nil {
		s.database.Close()
	}

	os.Remove(s.cfg.PidFile)
	os.Remove(s.cfg.SocketPath)

	log.Println("Daemon stopped")

	// Must be the last statement: Start() blocks on this channel so the
	// process cannot exit before the cleanup above has completed.
	close(s.shutdownDone)
}

func (s *Server) writePidFile() error {
	return os.WriteFile(s.cfg.PidFile, []byte(strconv.Itoa(os.Getpid())), 0644)
}

func Start(cfg *config.Config) error {
	dbPath := cfg.GetDatabasePath("")
	database, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	server := NewServer(cfg, database)
	return server.Start()
}

func Stop(cfg *config.Config) error {
	conn, err := net.Dial("unix", cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("daemon not running or cannot connect: %w", err)
	}
	defer conn.Close()

	msg, _ := NewMessage(MsgShutdown, nil)
	data, _ := EncodeMessage(msg)
	conn.Write(data)

	return nil
}

func Status(cfg *config.Config) (bool, int, error) {
	data, err := os.ReadFile(cfg.PidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil
		}
		return false, 0, err
	}

	pid, err := strconv.Atoi(string(data))
	if err != nil {
		return false, 0, nil
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false, 0, nil
	}

	err = process.Signal(syscall.Signal(0))
	if err != nil {
		os.Remove(cfg.PidFile)
		return false, 0, nil
	}

	return true, pid, nil
}
