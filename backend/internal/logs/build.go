package logs

import (
	"context"
	"time"

	"github.com/podium/podium/internal/storage"
)

// BuildLogLine is one Docker build log line for a deployment. It
// re-exports the storage layer's shape so the logs package owns its
// own response type; the API layer converts it to JSON.
type BuildLogLine struct {
	TS   string `json:"ts"`
	Line string `json:"line"`
}

// BuildLogSource is the subset of the storage layer the build-log
// service needs. The real storage.Queries satisfies this.
type BuildLogSource interface {
	LogLinesSince(ctx context.Context, deploymentID int64, since time.Time) ([]storage.LogLine, error)
}

// FetchBuildLogs reads the demuxed Docker build output for a
// deployment. The orchestrator streamed every line the builder
// produced into the deploy_log_lines table (M2), so this is a pure
// read — no kubernetes access, no buffering.
//
// If `since` is the zero value the call returns the full build log;
// otherwise it's used as a cursor (RFC3339Nano parsing happens
// upstream in the API layer).
func FetchBuildLogs(ctx context.Context, src BuildLogSource, deploymentID int64, since time.Time) ([]BuildLogLine, error) {
	if src == nil {
		return nil, nil
	}
	lines, err := src.LogLinesSince(ctx, deploymentID, since)
	if err != nil {
		return nil, err
	}
	out := make([]BuildLogLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, BuildLogLine{
			TS:   storage.FormatPodTS(l.TS),
			Line: l.Line,
		})
	}
	return out, nil
}
