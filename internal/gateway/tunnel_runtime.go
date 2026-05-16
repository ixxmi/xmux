package gateway

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"cloud-terminal/internal/config"
	"cloud-terminal/internal/edge"
	"cloud-terminal/internal/shellparse"
)

type tunnelRuntime struct {
	hub *tunnelHub
}

type tunnelInteractiveSession struct {
	client    *tunnelClient
	sessionID string
	done      chan edge.ExecResult
}

func newTunnelRuntime(hub *tunnelHub) *tunnelRuntime {
	return &tunnelRuntime{hub: hub}
}

func (r *tunnelRuntime) ResolveWorkbenchStart(opts workbenchStartOptions) (workbenchStartResolution, error) {
	client := r.hub.current()
	if client == nil {
		return workbenchStartResolution{}, tunnelUnavailable()
	}
	info := client.info()

	agentID := normalizeWorkbenchAgentID(opts.Agent)
	var selected workbenchAgentInfo
	for _, agent := range info.agents {
		if normalizeWorkbenchAgentID(agent.ID) == agentID {
			selected = agent
			break
		}
	}
	if selected.ID == "" {
		return workbenchStartResolution{}, fmt.Errorf("unsupported agent %q", opts.Agent)
	}
	if !selected.Enabled {
		return workbenchStartResolution{}, fmt.Errorf("%s is not enabled in local edge policy", selected.Label)
	}
	command := strings.TrimSpace(selected.Command)
	if command == "" {
		command = agentID
	}

	workDir := config.NormalizePath(opts.WorkDir)
	if workDir == "" {
		workDir = config.NormalizePath(info.workDir)
	}
	target := config.NormalizePath(opts.Target)
	var args []string
	if target != "" {
		if !pathWithinAllowed(target, info.allowPaths) {
			return workbenchStartResolution{}, errors.New("target is outside local edge allowed roots")
		}
		if workDir == "" || !pathWithinAllowed(workDir, info.allowPaths) {
			workDir = filepath.Dir(target)
		}
		args = []string{target}
	} else if workDir != "" && !pathWithinAllowed(workDir, info.allowPaths) {
		return workbenchStartResolution{}, errors.New("work_dir is outside local edge allowed roots")
	}
	if workDir == "" {
		return workbenchStartResolution{}, errors.New("work_dir is required")
	}

	return workbenchStartResolution{
		EdgeID:   firstNonEmpty(info.edgeID, "local-edge"),
		EdgeName: firstNonEmpty(info.edgeName, info.edgeID, "Local Edge"),
		Agent:    workbenchAgent{ID: agentID, Command: command},
		WorkDir:  workDir,
		Target:   target,
		Args:     slices.Clone(args),
	}, nil
}

func (r *tunnelRuntime) ParseAndExec(ctx context.Context, req edge.ExecRequest, line string) edge.ExecResult {
	parsed, err := shellparse.ParseLine(line)
	if err != nil {
		return edge.ExecResult{RequestID: req.RequestID, ExitCode: 126, Denied: true, Error: err.Error(), Stderr: err.Error()}
	}
	req.Command = parsed.Command
	req.Args = parsed.Args
	return r.Exec(ctx, req)
}

func (r *tunnelRuntime) Exec(_ context.Context, req edge.ExecRequest) edge.ExecResult {
	return edge.ExecResult{
		RequestID: req.RequestID,
		ExitCode:  126,
		Denied:    true,
		Error:     "non-interactive commands are not supported over tunnel runtime",
		Stderr:    "non-interactive commands are not supported over tunnel runtime\r\n",
	}
}

func (r *tunnelRuntime) ParseAndStartInteractive(ctx context.Context, req edge.ExecRequest, line string, opts edge.InteractiveOptions) (InteractiveSession, error) {
	parsed, err := shellparse.ParseLine(line)
	if err != nil {
		return nil, err
	}
	req.Command = parsed.Command
	req.Args = parsed.Args
	return r.StartInteractive(ctx, req, opts)
}

func (r *tunnelRuntime) StartInteractive(ctx context.Context, req edge.ExecRequest, _ edge.InteractiveOptions) (InteractiveSession, error) {
	client := r.hub.current()
	if client == nil {
		return nil, tunnelUnavailable()
	}
	agent := strings.TrimSpace(req.Command)
	if agent == "" {
		agent = "codex"
	}
	var out tunnelStartSessionResponse
	err := client.request(ctx, "start_session", tunnelStartSessionRequest{
		SessionID: req.SessionID,
		RequestID: req.RequestID,
		Agent:     agent,
		Command:   req.Command,
		WorkDir:   req.WorkDir,
		Target:    firstNonEmpty(req.Args...),
		Args:      req.Args,
		Rows:      req.Rows,
		Cols:      req.Cols,
	}, &out)
	if err != nil {
		return nil, err
	}
	done := make(chan edge.ExecResult, 1)
	session := &tunnelInteractiveSession{
		client:    client,
		sessionID: firstNonEmpty(out.SessionID, req.SessionID),
		done:      done,
	}
	exitCh := client.waitExit(session.sessionID)
	go func() {
		msg, ok := <-exitCh
		if ok {
			done <- edge.ExecResult{
				RequestID: req.RequestID,
				ExitCode:  msg.ExitCode,
				Duration:  msg.Duration,
				Error:     msg.Error,
				WorkDir:   msg.WorkDir,
			}
		} else {
			done <- edge.ExecResult{RequestID: req.RequestID, ExitCode: 1, Error: "tunnel disconnected"}
		}
		close(done)
	}()
	return session, nil
}

func (s *tunnelInteractiveSession) Write(data []byte) error {
	if s == nil || s.client == nil {
		return errors.New("tunnel session is closed")
	}
	return s.client.request(context.Background(), "input", tunnelInputRequest{SessionID: s.sessionID, Data: string(data)}, nil)
}

func (s *tunnelInteractiveSession) Resize(rows, cols uint16) error {
	if s == nil || s.client == nil {
		return errors.New("tunnel session is closed")
	}
	return s.client.request(context.Background(), "resize", tunnelResizeRequest{SessionID: s.sessionID, Rows: rows, Cols: cols}, nil)
}

func (s *tunnelInteractiveSession) Close() {
	if s == nil || s.client == nil {
		return
	}
	_ = s.client.request(context.Background(), "stop", tunnelStopRequest{SessionID: s.sessionID}, nil)
}

func (s *tunnelInteractiveSession) Done() <-chan edge.ExecResult {
	return s.done
}
