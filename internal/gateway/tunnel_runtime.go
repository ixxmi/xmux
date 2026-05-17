package gateway

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"cloud-terminal/internal/config"
	"cloud-terminal/internal/edge"
	"cloud-terminal/internal/policy"
	"cloud-terminal/internal/shellparse"
)

type tunnelRuntime struct {
	hub            *tunnelHub
	policyResolver interface {
		UserPolicy(string, policy.Config) (policy.Config, error)
	}
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
	client := r.clientForAccount(opts.Account)
	if client == nil {
		return workbenchStartResolution{}, tunnelUnavailable()
	}
	info := client.info()
	policyCfg := r.policyForAccount(opts.Account)

	agentID := normalizeWorkbenchAgentID(opts.Agent)
	var selected workbenchAgentInfo
	for _, agent := range filterWorkbenchAgentsForPolicy(info.agents, policyCfg) {
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

	workDir := config.NormalizePath(opts.WorkDir)
	if workDir == "" {
		workDir = defaultWorkbenchPath(info.workDir, policyCfg.AllowPaths, policyCfg.RequirePathMatch)
	}
	target := config.NormalizePath(opts.Target)
	var args []string
	if target != "" {
		if !pathWithinAllowed(target, policyCfg.AllowPaths) {
			return workbenchStartResolution{}, errors.New("target is outside local edge allowed roots")
		}
		if workDir == "" || !pathWithinAllowed(workDir, policyCfg.AllowPaths) {
			workDir = filepath.Dir(target)
		}
		args = []string{target}
	} else if workDir != "" && !pathWithinAllowed(workDir, policyCfg.AllowPaths) {
		return workbenchStartResolution{}, errors.New("work_dir is outside local edge allowed roots")
	}
	if workDir == "" {
		return workbenchStartResolution{}, errors.New("work_dir is required")
	}

	return workbenchStartResolution{
		EdgeID:   firstNonEmpty(info.edgeID, "local-edge"),
		EdgeName: firstNonEmpty(info.edgeName, info.edgeID, "Local Edge"),
		Agent:    workbenchAgent{ID: agentID, Command: firstNonEmpty(selected.Command, agentID)},
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
	client := r.clientForAccount(req.User)
	if client == nil {
		return nil, tunnelUnavailable()
	}
	agent := normalizeWorkbenchAgentID(req.Command)
	if agent == "" {
		agent = "codex"
	}
	bin := ""
	args := slices.Clone(req.Args)
	if len(args) > 0 && filepath.IsAbs(config.NormalizePath(args[0])) {
		bin = args[0]
		args = args[1:]
	}
	policyCfg := r.policyForAccount(req.User)
	var out tunnelStartSessionResponse
	err := client.request(ctx, "start_session", tunnelStartSessionRequest{
		SessionID:        req.SessionID,
		RequestID:        req.RequestID,
		Account:          req.User,
		Agent:            agent,
		Command:          agent,
		Bin:              bin,
		WorkDir:          req.WorkDir,
		Target:           firstPathArg(args),
		Args:             args,
		AllowPaths:       slices.Clone(policyCfg.AllowPaths),
		RequirePathMatch: policyCfg.RequirePathMatch,
		Rows:             req.Rows,
		Cols:             req.Cols,
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

func (r *tunnelRuntime) clientForAccount(account string) *tunnelClient {
	if r == nil || r.hub == nil {
		return nil
	}
	return r.hub.currentForAccount(account)
}

func (r *tunnelRuntime) SetUserPolicyResolver(resolver interface {
	UserPolicy(string, policy.Config) (policy.Config, error)
}) {
	r.policyResolver = resolver
}

func (r *tunnelRuntime) policyForAccount(account string) policy.Config {
	if r == nil || r.hub == nil {
		return policy.Config{}
	}
	cfg := r.hub.configSnapshot()
	if r.policyResolver == nil {
		return cfg.Policy
	}
	resolved, err := r.policyResolver.UserPolicy(account, cfg.Policy)
	if err != nil {
		return cfg.Policy
	}
	return resolved
}

func firstPathArg(args []string) string {
	for _, arg := range args {
		arg = config.NormalizePath(arg)
		if arg != "" && filepath.IsAbs(arg) {
			return arg
		}
	}
	return ""
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
