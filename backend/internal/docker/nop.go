package docker

import "context"

// NopBuilder is a docker.Builder that always returns Err. Used in
// `podium` boots where the docker daemon isn't reachable yet (e.g.
// running smoke tests in CI). The orchestrator treats the error as a
// normal build failure and the deployment transitions to FAILED.
type NopBuilder struct{ Err error }

func (n NopBuilder) Build(_ context.Context, _, _ string, _ LogSink) error { return n.Err }
func (n NopBuilder) LoadIntoKind(_ context.Context, _ string) error        { return n.Err }

// NopFetcher is the matching SourceFetcher.
type NopFetcher struct{ Err error }

func (n NopFetcher) Fetch(_ context.Context, _, _ string) error { return n.Err }
