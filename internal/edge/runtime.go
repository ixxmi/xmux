package edge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"cloud-terminal/internal/audit"
	"cloud-terminal/internal/policy"
	"cloud-terminal/internal/shellparse"

	"github.com/creack/pty"
)

type Options struct {
	PolicyProvider interface {
		PolicyEngine() (*policy.Engine, error)
	}
	Audit          audit.Writer
	DefaultEnv     map[string]string
	DefaultDir     string
	CommandTimeout time.Duration
	MaxOutputSize  int64
	Logger         *slog.Logger
}

type Runtime struct {
	policyProvider interface {
		PolicyEngine() (*policy.Engine, error)
	}
	audit          audit.Writer
	defaultEnv     map[string]string
	defaultDir     string
	commandTimeout time.Duration
	maxOutputSize  int64
	logger         *slog.Logger
}

type InteractiveSession struct {
	runtime   *Runtime
	request   ExecRequest
	decision  *policy.Decision
	cmd       *exec.Cmd
	ptmx      *os.File
	startedAt time.Time
	done      chan ExecResult
	closeOnce sync.Once
}

type InteractiveOptions struct {
	Output func([]byte)
}

type ExecRequest struct {
	RequestID string   `json:"request_id"`
	SessionID string   `json:"session_id"`
	User      string   `json:"user"`
	EdgeID    string   `json:"edge_id"`
	WorkDir   string   `json:"work_dir,omitempty"`
	Command   string   `json:"command"`
	Args      []string `json:"args"`
	Rows      uint16   `json:"rows"`
	Cols      uint16   `json:"cols"`
}

type ExecResult struct {
	RequestID string `json:"request_id"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Denied    bool   `json:"denied"`
	Error     string `json:"error,omitempty"`
	Duration  string `json:"duration"`
	WorkDir   string `json:"work_dir,omitempty"`
}

func NewRuntime(opts Options) *Runtime {
	if opts.CommandTimeout == 0 {
		opts.CommandTimeout = 30 * time.Second
	}
	if opts.MaxOutputSize == 0 {
		opts.MaxOutputSize = 1 << 20
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	defaultDir := cleanAbsDir(opts.DefaultDir)
	return &Runtime{
		policyProvider: opts.PolicyProvider,
		audit:          opts.Audit,
		defaultEnv:     opts.DefaultEnv,
		defaultDir:     defaultDir,
		commandTimeout: opts.CommandTimeout,
		maxOutputSize:  opts.MaxOutputSize,
		logger:         opts.Logger,
	}
}

func (r *Runtime) ParseAndExec(ctx context.Context, req ExecRequest, line string) ExecResult {
	parsed, err := shellparse.ParseLine(line)
	if err != nil {
		return r.denied(req, "", nil, err)
	}
	req.Command = parsed.Command
	req.Args = parsed.Args
	return r.Exec(ctx, req)
}

func (r *Runtime) ParseAndStartInteractive(ctx context.Context, req ExecRequest, line string, opts InteractiveOptions) (*InteractiveSession, error) {
	parsed, err := shellparse.ParseLine(line)
	if err != nil {
		result := r.denied(req, "", nil, err)
		return nil, errors.New(result.Stderr)
	}
	req.Command = parsed.Command
	req.Args = parsed.Args
	return r.StartInteractive(ctx, req, opts)
}

func (r *Runtime) Exec(ctx context.Context, req ExecRequest) ExecResult {
	start := time.Now()
	if req.Command == "cd" {
		return r.execCD(req, start)
	}

	decision, err := r.decide(req.Command, req.Args)
	if err != nil {
		return r.denied(req, req.Command, req.Args, err)
	}

	timeout := r.commandTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, decision.Bin, decision.Args...)
	cmd.Dir = r.workDir(req.WorkDir)
	cmd.Env = r.env()

	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{writer: &stderr, remaining: r.maxOutputSize}

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rowsOrDefault(req.Rows), Cols: colsOrDefault(req.Cols)})
	if err != nil {
		return r.failure(req, decision, start, stderr.String(), fmt.Errorf("start command: %w", err))
	}
	defer ptmx.Close()

	var stdout bytes.Buffer
	_, copyErr := io.Copy(&limitedWriter{writer: &stdout, remaining: r.maxOutputSize}, ptmx)
	waitErr := cmd.Wait()

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		killProcess(cmd.Process)
		waitErr = ctx.Err()
	}

	if copyErr != nil && !errors.Is(copyErr, os.ErrClosed) && !strings.Contains(copyErr.Error(), "input/output error") {
		r.logger.Debug("copy pty", "error", copyErr)
	}

	exitCode := 0
	if waitErr != nil {
		exitCode = exitCodeOf(waitErr)
	}

	result := ExecResult{
		RequestID: req.RequestID,
		Stdout:    stdout.String(),
		Stderr:    stderr.String(),
		ExitCode:  exitCode,
		Duration:  time.Since(start).String(),
		WorkDir:   cmd.Dir,
	}
	if waitErr != nil {
		result.Error = waitErr.Error()
	}

	r.writeAudit(audit.Record{
		Time:      time.Now(),
		SessionID: req.SessionID,
		RequestID: req.RequestID,
		User:      req.User,
		EdgeID:    req.EdgeID,
		Command:   decision.Command,
		Args:      slices.Clone(decision.Args),
		Allowed:   true,
		ExitCode:  exitCode,
		Duration:  result.Duration,
		Stdout:    trimAudit(result.Stdout),
		Stderr:    trimAudit(result.Stderr),
	})

	return result
}

func (r *Runtime) StartInteractive(ctx context.Context, req ExecRequest, opts InteractiveOptions) (*InteractiveSession, error) {
	decision, err := r.decide(req.Command, req.Args)
	if err != nil {
		result := r.denied(req, req.Command, req.Args, err)
		return nil, errors.New(result.Stderr)
	}
	if !decision.Interactive {
		err := fmt.Errorf("%w: %s is not allowed for interactive sessions", policy.ErrDenied, decision.Command)
		result := r.denied(req, req.Command, req.Args, err)
		return nil, errors.New(result.Stderr)
	}

	cmd := exec.CommandContext(ctx, decision.Bin, decision.Args...)
	cmd.Dir = r.workDir(req.WorkDir)
	cmd.Env = r.env()

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rowsOrDefault(req.Rows), Cols: colsOrDefault(req.Cols)})
	if err != nil {
		result := r.failure(req, decision, time.Now(), "", fmt.Errorf("start interactive command: %w", err))
		return nil, errors.New(result.Stderr)
	}

	session := &InteractiveSession{
		runtime:   r,
		request:   req,
		decision:  decision,
		cmd:       cmd,
		ptmx:      ptmx,
		startedAt: time.Now(),
		done:      make(chan ExecResult, 1),
	}

	r.writeAudit(audit.Record{
		Time:      session.startedAt,
		SessionID: req.SessionID,
		RequestID: req.RequestID,
		User:      req.User,
		EdgeID:    req.EdgeID,
		Command:   decision.Command,
		Args:      slices.Clone(decision.Args),
		Allowed:   true,
		Reason:    "interactive session started",
	})

	go session.readLoop(opts.Output)
	go session.waitLoop()

	return session, nil
}

func (r *Runtime) execCD(req ExecRequest, start time.Time) ExecResult {
	decision, err := r.decide(req.Command, req.Args)
	if err != nil {
		return r.denied(req, req.Command, req.Args, err)
	}
	if len(decision.Args) > 1 {
		err := fmt.Errorf("%w: cd accepts at most one path", policy.ErrDenied)
		return r.denied(req, req.Command, req.Args, err)
	}

	target := r.defaultDir
	if len(decision.Args) == 1 {
		target = decision.Args[0]
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(r.workDir(req.WorkDir), target)
	}
	target = filepath.Clean(target)
	if _, err := r.decide(req.Command, []string{target}); err != nil {
		return r.denied(req, req.Command, []string{target}, err)
	}

	info, err := os.Stat(target)
	if err != nil {
		return r.failure(req, decision, start, "", fmt.Errorf("cd: %w", err))
	}
	if !info.IsDir() {
		return r.failure(req, decision, start, "", fmt.Errorf("cd: %s is not a directory", target))
	}

	result := ExecResult{
		RequestID: req.RequestID,
		Stdout:    target + "\r\n",
		ExitCode:  0,
		Duration:  time.Since(start).String(),
		WorkDir:   target,
	}
	r.writeAudit(audit.Record{
		Time:      time.Now(),
		SessionID: req.SessionID,
		RequestID: req.RequestID,
		User:      req.User,
		EdgeID:    req.EdgeID,
		Command:   decision.Command,
		Args:      slices.Clone(decision.Args),
		Allowed:   true,
		ExitCode:  result.ExitCode,
		Duration:  result.Duration,
		Stdout:    trimAudit(result.Stdout),
	})
	return result
}

func (r *Runtime) decide(command string, args []string) (*policy.Decision, error) {
	if r.policyProvider == nil {
		return nil, fmt.Errorf("%w: policy provider is not configured", policy.ErrDenied)
	}
	engine, err := r.policyProvider.PolicyEngine()
	if err != nil {
		return nil, fmt.Errorf("load policy: %w", err)
	}
	return engine.Decide(command, args)
}

func (r *Runtime) workDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return r.defaultDir
	}
	return cleanAbsDir(dir)
}

func (s *InteractiveSession) Write(data []byte) error {
	if s == nil || s.ptmx == nil {
		return errors.New("interactive session is closed")
	}
	_, err := s.ptmx.Write(data)
	return err
}

func (s *InteractiveSession) Resize(rows, cols uint16) error {
	if s == nil || s.ptmx == nil {
		return errors.New("interactive session is closed")
	}
	return pty.Setsize(s.ptmx, &pty.Winsize{Rows: rowsOrDefault(rows), Cols: colsOrDefault(cols)})
}

func (s *InteractiveSession) Close() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		_ = s.ptmx.Close()
		killProcess(s.cmd.Process)
	})
}

func (s *InteractiveSession) Done() <-chan ExecResult {
	return s.done
}

func (s *InteractiveSession) readLoop(output func([]byte)) {
	if output == nil {
		_, _ = io.Copy(io.Discard, s.ptmx)
		return
	}

	buf := make([]byte, 16*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			output(chunk)
		}
		if err != nil {
			if !errors.Is(err, os.ErrClosed) && !strings.Contains(err.Error(), "input/output error") {
				s.runtime.logger.Debug("read interactive pty", "error", err)
			}
			return
		}
	}
}

func (s *InteractiveSession) waitLoop() {
	waitErr := s.cmd.Wait()
	exitCode := 0
	if waitErr != nil {
		exitCode = exitCodeOf(waitErr)
	}

	result := ExecResult{
		RequestID: s.request.RequestID,
		ExitCode:  exitCode,
		Duration:  time.Since(s.startedAt).String(),
	}
	if waitErr != nil {
		result.Error = waitErr.Error()
	}

	s.runtime.writeAudit(audit.Record{
		Time:      time.Now(),
		SessionID: s.request.SessionID,
		RequestID: s.request.RequestID,
		User:      s.request.User,
		EdgeID:    s.request.EdgeID,
		Command:   s.decision.Command,
		Args:      slices.Clone(s.decision.Args),
		Allowed:   true,
		Reason:    "interactive session ended",
		ExitCode:  exitCode,
		Duration:  result.Duration,
	})

	s.closeOnce.Do(func() {
		_ = s.ptmx.Close()
	})
	s.done <- result
	close(s.done)
}

func (r *Runtime) denied(req ExecRequest, command string, args []string, err error) ExecResult {
	result := ExecResult{
		RequestID: req.RequestID,
		Denied:    true,
		ExitCode:  126,
		Error:     err.Error(),
		Stderr:    "denied: " + err.Error() + "\r\n",
	}
	r.writeAudit(audit.Record{
		Time:      time.Now(),
		SessionID: req.SessionID,
		RequestID: req.RequestID,
		User:      req.User,
		EdgeID:    req.EdgeID,
		Command:   command,
		Args:      slices.Clone(args),
		Allowed:   false,
		Reason:    err.Error(),
		ExitCode:  result.ExitCode,
	})
	return result
}

func (r *Runtime) failure(req ExecRequest, decision *policy.Decision, start time.Time, stderr string, err error) ExecResult {
	result := ExecResult{
		RequestID: req.RequestID,
		Stderr:    "error: " + err.Error() + "\r\n",
		Error:     err.Error(),
		ExitCode:  1,
		Duration:  time.Since(start).String(),
	}
	r.writeAudit(audit.Record{
		Time:      time.Now(),
		SessionID: req.SessionID,
		RequestID: req.RequestID,
		User:      req.User,
		EdgeID:    req.EdgeID,
		Command:   decision.Command,
		Args:      slices.Clone(decision.Args),
		Allowed:   true,
		Reason:    err.Error(),
		ExitCode:  result.ExitCode,
		Duration:  result.Duration,
		Stderr:    trimAudit(stderr),
	})
	return result
}

func (r *Runtime) env() []string {
	env := os.Environ()
	for key, value := range r.defaultEnv {
		env = append(env, key+"="+value)
	}
	return env
}

func (r *Runtime) writeAudit(record audit.Record) {
	if r.audit == nil {
		return
	}
	if err := r.audit.Write(record); err != nil {
		r.logger.Warn("write audit", "error", err)
	}
}

func rowsOrDefault(rows uint16) uint16 {
	if rows == 0 {
		return 24
	}
	return rows
}

func colsOrDefault(cols uint16) uint16 {
	if cols == 0 {
		return 100
	}
	return cols
}

func exitCodeOf(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return 124
	}
	return 1
}

func killProcess(process *os.Process) {
	if process == nil {
		return
	}
	_ = process.Kill()
}

func trimAudit(value string) string {
	const limit = 4096
	value = strings.TrimRight(value, "\x00")
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n...[truncated]"
}

func cleanAbsDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		dir = "."
	}
	if abs, err := filepath.Abs(dir); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(dir)
}

type limitedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	originalLen := len(p)
	if w.remaining <= 0 {
		return originalLen, nil
	}
	if int64(len(p)) > w.remaining {
		p = p[:w.remaining]
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	if err != nil {
		return n, err
	}
	return originalLen, nil
}
