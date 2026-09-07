package ingress

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRunner simulates `kubectl port-forward`: it picks a free
// localhost port, opens a TCP listener, and pipes bytes between any
// incoming connection and a backend test server. The stderr pipe
// emits the readiness line "Forwarding from 127.0.0.1:<port> -> ..."
// after a configurable delay so tests can exercise the race.
type fakeRunner struct {
	backend       net.Listener // optional; if nil, the fake just accepts and closes
	readyDelay    time.Duration
	startFailNext bool
	// dialUpstream, if set, makes proxyConn dial this address instead
	// of using `backend`. Proxy tests use this to point the fake at
	// an httptest.Server.
	dialUpstream string
	// readyListener, if true, makes the fake use a real TCP listener
	// on the requested local port (so DialContext succeeds).
	// Otherwise the fake just emits the readiness line and returns
	// the channel — DialContext would fail because nothing is listening.
	readyListener bool

	mu      sync.Mutex
	cancels []context.CancelFunc
	exiteds []chan struct{}
}

func (f *fakeRunner) Start(_ context.Context, name string, args ...string) (io.ReadCloser, io.ReadCloser, context.CancelFunc, <-chan struct{}, error) {
	if f.startFailNext {
		f.startFailNext = false
		return nil, nil, nil, nil, errors.New("fake: refusing to start")
	}

	// Pull local port from "kubectl port-forward svc/<svc> <local>:<remote>"
	var localPort int
	for _, a := range args {
		if strings.Contains(a, ":") {
			parts := strings.SplitN(a, ":", 2)
			localPort, _ = strconv.Atoi(parts[0])
			break
		}
	}
	if localPort == 0 {
		return nil, nil, nil, nil, errors.New("fake: could not parse local port")
	}

	var ln net.Listener
	var err error
	if f.readyListener || f.backend != nil || f.dialUpstream != "" {
		ln, err = net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(localPort))
		if err != nil {
			return nil, nil, nil, nil, err
		}
	}

	exited := make(chan struct{})
	f.mu.Lock()
	f.exiteds = append(f.exiteds, exited)
	f.mu.Unlock()

	// stderr pipe: write the readiness line on a delay (so tests can
	// race Start against it). Closing the pipe signals subprocess exit.
	stderrR, stderrW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()

	cancel := func() {
		if ln != nil {
			_ = ln.Close()
		}
		_ = stderrW.Close()
		_ = stdoutW.Close()
		// Close `exited` so the Forwarder's bridge goroutine can move
		// on. Tests that don't close it via ln-close will still get
		// `exited` closed when the stderr/stdout drain goroutines
		// hit EOF, but in practice the explicit close is what callers
		// depend on. Idempotent via the `select`.
		select {
		case <-exited:
		default:
			close(exited)
		}
	}

	go func() {
		<-time.After(f.readyDelay)
		_, _ = io.WriteString(stderrW, "Forwarding from 127.0.0.1:"+strconv.Itoa(localPort)+" -> 8080\n")
		// Hold stderr open until cancel.
		<-exited
		_ = stderrW.Close()
	}()

	if ln != nil {
		go func() {
			defer close(exited)
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go f.proxyConn(c)
			}
		}()
	} else {
		// No listener (test fake). The Forwarder's bridge goroutine
		// waits on `<-exited`. We don't close it here — the test
		// calls cancel() which closes stderr and exits the readiness
		// goroutine; without ln.Accept() there's no other goroutine
		// that would close exited. Tests using this mode must Stop()
		// the forwarder explicitly.
		go func() {
			<-exited // never fires unless something closes it
		}()
	}

	f.mu.Lock()
	f.cancels = append(f.cancels, cancel)
	f.mu.Unlock()

	return stdoutR, stderrR, cancel, exited, nil
}

func (f *fakeRunner) proxyConn(c net.Conn) {
	defer c.Close()

	var upstream net.Conn
	var err error
	switch {
	case f.dialUpstream != "":
		upstream, err = net.Dial("tcp", f.dialUpstream)
	case f.backend != nil:
		upstream, err = net.Dial("tcp", f.backend.Addr().String())
	default:
		return
	}
	if err != nil {
		return
	}
	defer upstream.Close()
	go io.Copy(upstream, c)
	io.Copy(c, upstream)
}

func (f *fakeRunner) KillAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.cancels {
		c()
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("freePort: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// backendServer starts a one-shot http server on a random port; the
// test uses its address as the "upstream" the fake port-forwards to.
func backendServer(t *testing.T, handler func(net.Conn)) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("backendServer: %v", err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handler(c)
		}
	}()
	return ln
}

func TestForwarder_StartStopHappyPath(t *testing.T) {
	port := freePort(t)
	be := backendServer(t, func(c net.Conn) {
		_, _ = io.Copy(io.Discard, c)
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok")
		c.Close()
	})
	defer be.Close()

	fake := &fakeRunner{backend: be, readyDelay: 5 * time.Millisecond}
	f := NewForwarder("podium-dev", "my-api-x1", port, 8080, nil)
	f.SetRunner(fake)
	f.SetReadyTimeout(2 * time.Second)

	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := f.LocalPort(); got != port {
		t.Errorf("LocalPort=%d want %d", got, port)
	}
	if err := f.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

func TestForwarder_StartIsIdempotent(t *testing.T) {
	port := freePort(t)
	fake := &fakeRunner{readyDelay: 5 * time.Millisecond}
	f := NewForwarder("podium-dev", "svc", port, 8080, nil)
	f.SetRunner(fake)
	f.SetReadyTimeout(2 * time.Second)

	if err := f.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Second Start should not error and should not crash.
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if err := f.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestForwarder_StartTimeout(t *testing.T) {
	port := freePort(t)
	fake := &fakeRunner{readyDelay: 5 * time.Second} // never ready in time
	f := NewForwarder("podium-dev", "svc", port, 8080, nil)
	f.SetRunner(fake)
	f.SetReadyTimeout(50 * time.Millisecond)

	err := f.Start(context.Background())
	if err == nil {
		t.Fatal("Start should time out")
	}
	if !strings.Contains(err.Error(), "did not become ready") {
		t.Errorf("err=%v want timeout", err)
	}
	// Cleanup.
	_ = f.Stop()
}

func TestForwarder_StartPropagatesRunnerError(t *testing.T) {
	port := freePort(t)
	fake := &fakeRunner{startFailNext: true}
	f := NewForwarder("podium-dev", "svc", port, 8080, nil)
	f.SetRunner(fake)
	f.SetReadyTimeout(50 * time.Millisecond)

	err := f.Start(context.Background())
	if err == nil {
		t.Fatal("Start should propagate runner error")
	}
	if !strings.Contains(err.Error(), "refusing to start") {
		t.Errorf("err=%v want runner error", err)
	}
}

func TestForwarder_DialContextAfterStart(t *testing.T) {
	port := freePort(t)
	be := backendServer(t, func(c net.Conn) {
		// Read one byte (the GET), write a tiny response, close.
		buf := make([]byte, 64)
		n, _ := c.Read(buf)
		_ = n
		_, _ = io.WriteString(c, "hi")
		c.Close()
	})
	defer be.Close()

	fake := &fakeRunner{backend: be, readyDelay: 5 * time.Millisecond}
	f := NewForwarder("podium-dev", "svc", port, 8080, nil)
	f.SetRunner(fake)
	f.SetReadyTimeout(2 * time.Second)

	if err := f.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer f.Stop()

	conn, err := f.DialContext(context.Background(), "tcp", "ignored")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("x"))
	buf := make([]byte, 16)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read upstream: %v", err)
	}
	if string(buf[:n]) != "hi" {
		t.Errorf("upstream body=%q want %q", buf[:n], "hi")
	}
}

func TestForwarder_StopIsIdempotent(t *testing.T) {
	port := freePort(t)
	fake := &fakeRunner{readyDelay: 5 * time.Millisecond}
	f := NewForwarder("podium-dev", "svc", port, 8080, nil)
	f.SetRunner(fake)
	f.SetReadyTimeout(time.Second)

	if err := f.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := f.Stop(); err != nil {
		t.Fatal(err)
	}
	// Second Stop should not panic and should return nil.
	if err := f.Stop(); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestForwarder_StopBeforeStartIsNoOp(t *testing.T) {
	f := NewForwarder("podium-dev", "svc", freePort(t), 8080, nil)
	if err := f.Stop(); err != nil {
		t.Errorf("Stop before Start: %v", err)
	}
}
