package daemon

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/Bakaface/sakusen/internal/task"
	"github.com/Bakaface/sakusen/internal/workflow"
)

func (s *Server) handleListAgents(conn net.Conn) {
	agents := s.manager.ListAgents()
	infos := make([]AgentInfo, len(agents))

	for i, a := range agents {
		infos[i] = agentToInfo(a)
	}

	s.sendMessage(conn, MsgAgentList, AgentListResponse{Agents: infos})
}

func (s *Server) handleStartAgent(conn net.Conn, req StartAgentRequest) {
	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to get task: %v", err))
		return
	}

	if t.Status != task.StatusPending && t.Status != task.StatusAwaitingApproval && t.Status != task.StatusTmux {
		s.sendError(conn, fmt.Sprintf("task is not startable (status: %s)", t.Status))
		return
	}

	if err := s.startTaskAgent(t); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to start agent: %v", err))
		return
	}

	s.sendMessage(conn, MsgOK, OKResponse{Message: "agent started"})
}

func (s *Server) handleStopAgent(conn net.Conn, req StopAgentRequest) {
	if err := s.manager.StopAgent(req.AgentID); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to stop agent: %v", err))
		return
	}

	s.sendMessage(conn, MsgOK, OKResponse{Message: "agent stopped"})
}

func (s *Server) handleStopTask(conn net.Conn, req StopTaskRequest) {
	agentID := strconv.FormatInt(req.TaskID, 10)
	if err := s.manager.StopAgent(agentID); err != nil {
		s.sendError(conn, fmt.Sprintf("failed to stop task: %v", err))
		return
	}

	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendError(conn, fmt.Sprintf("failed to load task after stop: %v", err))
		return
	}
	s.broadcastToSubscribers(MsgTaskUpdate, TaskUpdateResponse{Task: s.taskToInfo(t)})
	s.sendMessage(conn, MsgStopTask, StopTaskResponse{Task: s.taskToInfo(t)})
}

func (s *Server) handleGetOutput(conn net.Conn, req GetOutputRequest) {
	lines, total, err := s.manager.GetOutput(req.AgentID, req.FromLine)
	if err != nil {
		taskID, parseErr := strconv.ParseInt(req.AgentID, 10, 64)
		if parseErr == nil {
			t, getErr := s.database.GetTask(taskID)
			if getErr == nil {
				dataDir := s.getProjectDataDir(t)
				allLines := readLogFile(workflow.ProjectLogPath(dataDir, taskID))
				total = len(allLines)
				if req.FromLine < total {
					lines = allLines[req.FromLine:]
				}
			}
		}
	}

	s.sendMessage(conn, MsgOutputChunk, OutputChunkResponse{
		AgentID:    req.AgentID,
		Lines:      lines,
		TotalLines: total,
	})
}

func (s *Server) handleSubscribe(conn net.Conn) {
	s.mu.Lock()
	s.subscribers[conn] = true
	s.mu.Unlock()

	s.sendMessage(conn, MsgOK, OKResponse{Message: "subscribed"})
}

func (s *Server) handleUnsubscribe(conn net.Conn) {
	s.mu.Lock()
	delete(s.subscribers, conn)
	s.mu.Unlock()

	s.sendMessage(conn, MsgOK, OKResponse{Message: "unsubscribed"})
}

func (s *Server) handleGetLogs(conn net.Conn, req GetLogsRequest) {
	t, err := s.database.GetTask(req.TaskID)
	if err != nil {
		s.sendMessage(conn, MsgGetLogs, GetLogsResponse{
			TaskID: req.TaskID,
			Lines:  []string{},
		})
		return
	}

	dataDir := s.getProjectDataDir(t)
	allLines := readLogFile(workflow.ProjectLogPath(dataDir, req.TaskID))
	totalLines := len(allLines)

	if req.Offset > 0 {
		if req.Offset >= len(allLines) {
			allLines = nil
		} else {
			allLines = allLines[req.Offset:]
		}
	}

	if req.Tail > 0 && len(allLines) > req.Tail {
		allLines = allLines[len(allLines)-req.Tail:]
	}

	s.sendMessage(conn, MsgGetLogs, GetLogsResponse{
		TaskID:     req.TaskID,
		Lines:      allLines,
		TotalLines: totalLines,
	})
}

func readLogFile(path string) []string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}
