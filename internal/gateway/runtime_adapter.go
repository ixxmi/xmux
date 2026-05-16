package gateway

import (
	"context"

	"cloud-terminal/internal/edge"
	"cloud-terminal/internal/policy"
)

type localRuntimeAdapter struct {
	runtime *edge.Runtime
}

func NewLocalRuntime(runtime *edge.Runtime) Runtime {
	if runtime == nil {
		return nil
	}
	return &localRuntimeAdapter{runtime: runtime}
}

func (r *localRuntimeAdapter) ParseAndExec(ctx context.Context, req edge.ExecRequest, line string) edge.ExecResult {
	return r.runtime.ParseAndExec(ctx, req, line)
}

func (r *localRuntimeAdapter) Exec(ctx context.Context, req edge.ExecRequest) edge.ExecResult {
	return r.runtime.Exec(ctx, req)
}

func (r *localRuntimeAdapter) ParseAndStartInteractive(ctx context.Context, req edge.ExecRequest, line string, opts edge.InteractiveOptions) (InteractiveSession, error) {
	return r.runtime.ParseAndStartInteractive(ctx, req, line, opts)
}

func (r *localRuntimeAdapter) StartInteractive(ctx context.Context, req edge.ExecRequest, opts edge.InteractiveOptions) (InteractiveSession, error) {
	return r.runtime.StartInteractive(ctx, req, opts)
}

func (r *localRuntimeAdapter) SetUserPolicyResolver(resolver interface {
	UserPolicy(string, policy.Config) (policy.Config, error)
}) {
	r.runtime.SetUserPolicyResolver(resolver)
}
