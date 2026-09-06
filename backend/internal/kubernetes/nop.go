package kubernetes

import (
	"context"
	"fmt"

	"github.com/podium/podium/internal/deployment"
)

// NopApplier is the fallback wired by cmd/podium/main.go when no
// kubeconfig is available. Its Apply returns an error wrapped with
// deployment.ErrDeployFailed so the orchestrator marks the deployment
// FAILED with the "deploy_failed" reason. The underlying NopApplier.Err
// (if set) is appended so the user sees "deploy failed: <kubeconfig
// error>". This mirrors the docker NopBuilder/NopFetcher pattern from
// M2 — boot never blocks on infrastructure the user hasn't set up
// yet.
type NopApplier struct {
	Err error
}

// Apply returns deployment.ErrDeployFailed wrapped with NopApplier.Err
// (when set) so classifyReason surfaces the reason string and the
// underlying kubeconfig error in the log stream.
func (n NopApplier) Apply(_ context.Context, _ int64) error {
	if n.Err == nil {
		return deployment.ErrDeployFailed
	}
	return fmt.Errorf("%w: %v", deployment.ErrDeployFailed, n.Err)
}
