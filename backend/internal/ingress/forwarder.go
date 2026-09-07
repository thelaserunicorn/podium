package ingress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Forwarder keeps a single `kubectl port-forward` subprocess alive for
// the (namespace, service, localPort, remotePort) tuple it was
// constructed with. The subprocess is started lazily and reconnected
// on demand. The HTTP reverse proxy in this package dials through the
// forwarder's local port per request.
//
// Why a subprocess rather than the Go k8s port-forward client? The
// Kubernetes Go client does not ship a port-forward transport; the
// supported universal path is shelling out to `kubectl`. Podium
// already requires the user to have a working kubectl/cluster setup,
// so this is a no-new-dependency bridge from the host to a kind
// ClusterIP service.
//
// Lifecycle:
//
//	f := &Forwarder{...}
//	if err := f.Start(ctx); err != nil { ... }    // spawn kubectl, wait for readiness
//	conn, err := f.DialContext(ctx, "tcp", "...") // per-request upstream dial
//	...
//	f.Stop()                                     // kill kubectl
type Forwarder struct {
	ns         string
	svc        string
	localPort  int
	remotePort int

	// runner abstracts subprocess invocation so tests can swap it.
	runner forwardRunner

	// readyTimeout bounds how long Start waits for the subprocess to
	// print its "Forwarding from 127.0.0.1:..." readiness line before
	// it considers the forwarder failed to come up.
	readyTimeout time.Duration

	// log receives subprocess stderr (kubectl's logs are chatty and
	// rarely useful in production, but they explain "connection
	// refused" mysteries when they happen).
	log *slog.Logger

	mu      sync.Mutex
	cmd     *runningCmd // nil before Start; replaced on restart
	lastErr error
}

// runningCmd is the slice of the subprocess lifecycle the Forwarder
// owns. Cancel() asks the subprocess to exit; Done() closes once it
// has actually exited (whether Cancel was called or the subprocess
// died on its own).
type runningCmd struct {
	Cancel context.CancelFunc
	Done   <-chan struct{}
}

// NewForwarder constructs a Forwarder. The caller decides which port
// to use; the Router picks per-route ports in slice 2.
//
// `localPort` must not be in use at the time Start is called. kubectl
// will pick the next free port if you pass 0, but pinning the port is
// how the Router caches routes by (appID, namespace) → port, so the
// proxy's DialContext can target it deterministically.
func NewForwarder(ns, svc string, localPort, remotePort int, log *slog.Logger) *Forwarder {
	if log == nil {
		log = slog.Default()
	}
	return &Forwarder{
		ns:           ns,
		svc:          svc,
		localPort:    localPort,
		remotePort:   remotePort,
		runner:       osRunner{},
		readyTimeout: 5 * time.Second,
		log:          log,
	}
}

// SetRunner swaps the subprocess runner. Tests use this to inject a
// fake `kubectl` that opens a local TCP listener without contacting a
// real cluster.
func (f *Forwarder) SetRunner(r forwardRunner) { f.runner = r }

// SetReadyTimeout bounds Start's wait for readiness. Zero keeps the
// default (5s); tests shorten this.
func (f *Forwarder) SetReadyTimeout(d time.Duration) {
	if d > 0 {
		f.readyTimeout = d
	}
}

// LocalPort returns the port kubectl binds on the host. Useful for
// tests and for the Router's port allocator.
func (f *Forwarder) LocalPort() int { return f.localPort }

// Start launches the kubectl port-forward subprocess and blocks
// until it signals readiness (by printing
// `Forwarding from 127.0.0.1:<local> -> ...` to stderr) or until
// `ctx` expires.
//
// Idempotent: if a subprocess is already running, Start is a no-op
// and returns nil.
func (f *Forwarder) Start(ctx context.Context) error {
	f.mu.Lock()
	if f.cmd != nil && !isClosed(f.cmd.Done) {
		f.mu.Unlock()
		return nil
	}
	// Stale cmd from a previous subprocess; clear it so the new one
	// takes its place.
	f.cmd = nil
	f.mu.Unlock()

	args := []string{
		"port-forward",
		"-n", f.ns,
		"svc/" + f.svc,
		fmt.Sprintf("%d:%d", f.localPort, f.remotePort),
	}
	stdout, stderr, cancel, exited, err := f.runner.Start(ctx, "kubectl", args...)
	if err != nil {
		return fmt.Errorf("ingress: start kubectl port-forward: %w", err)
	}

	// doneCh is closed exactly once when either:
	//   - the subprocess exits on its own (runner closes `exited`), OR
	//   - the caller invokes Cancel().
	//
	// We achieve that by wrapping cancel in a function that also
	// closes doneCh; the bridge goroutine waits on `exited` and
	// closes doneCh on natural exit.
	doneCh := make(chan struct{})
	wrappedCancel := func() {
		cancel()
		select {
		case <-doneCh:
			// already closed (by natural exit)
		default:
			close(doneCh)
		}
	}
	cmd := &runningCmd{
		Cancel: wrappedCancel,
		Done:   doneCh,
	}

	// Bridge: when the runner reports the subprocess has exited,
	// close doneCh so subsequent Dial/Stop calls see the dead state.
	go func() {
		<-exited
		select {
		case <-doneCh:
			// already closed (by wrappedCancel)
		default:
			close(doneCh)
		}
	}()

	f.mu.Lock()
	f.cmd = cmd
	f.lastErr = nil
	f.mu.Unlock()

	// Capture every line kubectl writes to stderr so we can surface
	// the real reason when it dies before becoming ready. Without
	// this the user only sees "exited before becoming ready" with no
	// clue that the Service is in the wrong namespace or doesn't
	// exist. We still log each line at debug in the watch goroutine
	// (so the operator sees the same picture); the buffer is for the
	// error path only.
	stderrBuf := &lineBuffer{max: 16}
	// The watch goroutine parses stderr for the "Forwarding from"
	// marker and closes `ready` once seen. It also drains stdout so
	// the subprocess never blocks on a full pipe.
	ready := make(chan struct{})
	go f.watch(stdout, stderr, stderrBuf, ready)

	select {
	case <-ready:
		return nil
	case <-doneCh:
		tail := strings.TrimSpace(stderrBuf.String())
		if tail != "" {
			return fmt.Errorf("ingress: kubectl port-forward exited before becoming ready: %s", tail)
		}
		return errors.New("ingress: kubectl port-forward exited before becoming ready")
	case <-ctx.Done():
		cancel()
		return fmt.Errorf("ingress: kubectl port-forward startup: %w", ctx.Err())
	case <-time.After(f.readyTimeout):
		cancel()
		return fmt.Errorf("ingress: kubectl port-forward did not become ready within %s", f.readyTimeout)
	}
}

// DialContext returns a net.Conn to 127.0.0.1:f.localPort. It is the
// http.Transport.DialContext the reverse proxy uses.
//
// If the subprocess has died since the last successful Start, Dial
// transparently restarts it. If the restart fails, Dial returns the
// error from the most recent Start attempt.
func (f *Forwarder) DialContext(ctx context.Context, network, _ string) (net.Conn, error) {
	f.mu.Lock()
	dead := f.cmd == nil || isClosed(f.cmd.Done)
	lastErr := f.lastErr
	f.mu.Unlock()

	if dead {
		if lastErr != nil {
			// Don't retry forever on a misconfigured forwarder
			// (e.g. wrong namespace); surface the original error so
			// the proxy can return 502 to the user.
			return nil, lastErr
		}
		if err := f.Start(ctx); err != nil {
			f.mu.Lock()
			f.lastErr = err
			f.mu.Unlock()
			return nil, err
		}
	}

	d := net.Dialer{Timeout: 5 * time.Second}
	return d.DialContext(ctx, network, fmt.Sprintf("127.0.0.1:%d", f.localPort))
}

// Stop kills the kubectl subprocess and waits for it to exit. Safe to
// call multiple times; safe to call before Start.
func (f *Forwarder) Stop() error {
	f.mu.Lock()
	cmd := f.cmd
	f.cmd = nil
	f.mu.Unlock()
	if cmd == nil {
		return nil
	}
	cmd.Cancel()
	<-cmd.Done
	return nil
}

// watch is the lifecycle goroutine: drain stderr to the logger and
// close `ready` once we see the readiness marker. stdout is drained
// to avoid back-pressuring the subprocess. The goroutine returns when
// stderr is closed (the subprocess exited and the OS closed its end
// of the pipe).
//
// stderrBuf, when non-nil, receives every line for error reporting.
// The buffer is only consulted on the failure path; on the success
// path (forwarder reached readiness) we don't surface kubectl's
// chatter to the user.
func (f *Forwarder) watch(stdout, stderr io.ReadCloser, stderrBuf *lineBuffer, ready chan struct{}) {
	// kubectl writes the readiness line ("Forwarding from 127.0.0.1:<port> -> ...")
	// to STDOUT, not stderr. We must parse stdout for the marker or Start
	// will hang until readyTimeout and return a useless "did not become
	// ready" error. stderr is still drained for diagnostics (real errors
	// like "NotFound" come on stderr).
	//
	// Both streams are line-buffered in parallel so a slow stderr
	// producer can't back-pressure the kubectl process.
	go func() {
		s := bufio.NewScanner(stdout)
		s.Buffer(make([]byte, 0, 4096), 64*1024)
		for s.Scan() {
			line := s.Text()
			f.log.Debug("kubectl port-forward stdout", "ns", f.ns, "svc", f.svc, "line", line)
			if strings.Contains(line, fmt.Sprintf("Forwarding from 127.0.0.1:%d", f.localPort)) {
				select {
				case <-ready:
					// Already closed.
				default:
					close(ready)
				}
				return
			}
		}
	}()

	go func() {
		s := bufio.NewScanner(stderr)
		s.Buffer(make([]byte, 0, 4096), 64*1024)
		for s.Scan() {
			line := s.Text()
			f.log.Debug("kubectl port-forward stderr", "ns", f.ns, "svc", f.svc, "line", line)
			if stderrBuf != nil {
				stderrBuf.append(line)
			}
		}
	}()
}

// lineBuffer is a small bounded ring of recent lines, used to surface
// kubectl's last words in the error path. Not safe for concurrent use;
// only one goroutine (the watch loop) writes.
type lineBuffer struct {
	mu  sync.Mutex
	buf []string
	max int
}

func (b *lineBuffer) append(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, line)
	if len(b.buf) > b.max {
		// Drop the oldest entries.
		b.buf = b.buf[len(b.buf)-b.max:]
	}
}

func (b *lineBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.buf, " | ")
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// forwardRunner is the abstraction over `kubectl port-forward`. The
// real implementation shells out to kubectl; tests inject a fake that
// opens a local TCP listener and writes the readiness line.
//
// `cancel` is invoked when the caller wants the subprocess to exit;
// `exited` closes once the subprocess has actually exited (either
// because cancel was called or because the subprocess died on its
// own).
type forwardRunner interface {
	Start(ctx context.Context, name string, args ...string) (stdout, stderr io.ReadCloser, cancel context.CancelFunc, exited <-chan struct{}, err error)
}

// osRunner is the production forwardRunner.
type osRunner struct{}

func (osRunner) Start(ctx context.Context, name string, args ...string) (io.ReadCloser, io.ReadCloser, context.CancelFunc, <-chan struct{}, error) {
	cmd := exec.Command(name, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("ingress: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("ingress: stderr pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("ingress: start %s: %w", name, err)
	}
	// Caller-controlled cancel: when invoked, we kill the subprocess.
	// The caller's ctx is honoured by exec.CommandContext — but we
	// want our own cancel that the Forwarder can drive independently
	// of the caller's lifetime (Stop must work after the caller ctx
	// has expired).
	cancel := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	return stdout, stderr, cancel, exited, nil
}

// ErrForwarderClosed indicates Stop was called before Start, or the
// forwarder has been torn down.
var ErrForwarderClosed = errors.New("ingress: forwarder closed")
