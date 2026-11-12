package network

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os/exec"
	"syscall"
	"time"
)

// Supervisor keeps one network protection process running for fltr.
//
// It starts network protection in its own session, so where a fltr crash
// doesn't end the session (outside a container), network protection keeps
// steering and filtering, and the restarted fltr finds it through the control
// socket and attaches to it instead of starting another. Its log lines are
// passed to Logf. When the context ends (fltr is stopped on purpose), network
// protection is asked to stop and restore the clients' ARP caches.
type Supervisor struct {
	Socket  string                           // control socket name
	Command func() *exec.Cmd                 // runs network protection with -supervised
	Logf    func(format string, args ...any) // fltr's log
	// Backoff is the first wait before restarting network protection after it
	// exits; it doubles, up to five minutes, while it keeps exiting quickly.
	Backoff time.Duration
}

const (
	maxBackoff  = 5 * time.Minute
	startupWait = 10 * time.Second
	stopWait    = 15 * time.Second
)

// Run supervises network protection until ctx ends, then stops it.
func (s *Supervisor) Run(ctx context.Context) {
	backoff := s.Backoff
	for {
		conn, exited, err := s.connect(ctx)
		if ctx.Err() != nil {
			if conn != nil {
				conn.Close()
			}
			s.stop(exited)
			return
		}
		if err != nil {
			s.Logf("network: %v; retrying in %s", err, backoff)
			if !sleep(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		started := time.Now()
		ended := make(chan struct{})
		go func() {
			defer close(ended)
			sc := bufio.NewScanner(conn)
			for sc.Scan() {
				s.Logf("network: %s", sc.Text())
			}
		}()
		select {
		case <-ended:
			conn.Close()
			how := "stopped"
			if exited != nil {
				how = "exited (" + exitStatus(<-exited) + ")"
			}
			if time.Since(started) > time.Minute {
				backoff = s.Backoff
			}
			s.Logf("network protection %s; restarting in %s", how, backoff)
			if !sleep(ctx, backoff) {
				return
			}
			backoff = min(backoff*2, maxBackoff)
		case <-ctx.Done():
			s.stop(exited)
			conn.Close()
			<-ended
			return
		}
	}
}

// connect attaches to a running network protection, starting one if there is none.
// exited reports when the network protection we started ends; it is nil when we
// attached to one already running.
func (s *Supervisor) connect(ctx context.Context) (conn *net.UnixConn, exited chan error, err error) {
	if conn, err := s.subscribe(); err == nil {
		s.Logf("network protection already running; attached to it")
		return conn, nil, nil
	}
	cmd := s.Command()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	exited = make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.Now().Add(startupWait)
	for {
		if conn, err := s.subscribe(); err == nil {
			return conn, exited, nil
		}
		select {
		case err := <-exited:
			var ee *exec.ExitError
			if errors.As(err, &ee) && ee.ExitCode() == ExitRunning {
				return nil, nil, errors.New("another network protection process holds the control socket")
			}
			return nil, nil, errors.New("exited at startup: " + exitStatus(err))
		case <-ctx.Done():
			// Not steering yet, so SIGTERM just ends it.
			cmd.Process.Signal(syscall.SIGTERM)
			<-exited
			return nil, nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			cmd.Process.Kill()
			<-exited
			return nil, nil, errors.New("control socket did not come up")
		}
	}
}

// subscribe connects to the control socket and asks for log lines.
func (s *Supervisor) subscribe() (*net.UnixConn, error) {
	conn, err := Dial(s.Socket)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Write([]byte("logs\n")); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

// stop asks network protection to stop and waits for it, if we started it (exited
// is not nil), or until its control socket closes.
func (s *Supervisor) stop(exited chan error) {
	conn, err := Dial(s.Socket)
	if err != nil {
		if exited != nil {
			<-exited
		}
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(stopWait))
	conn.Write([]byte("stop\n"))
	bufio.NewReader(conn).ReadString('\n')
	s.Logf("network: asked to stop")
	if exited == nil {
		// Not our child: wait for its socket to go away.
		deadline := time.Now().Add(stopWait)
		for time.Now().Before(deadline) {
			c, err := Dial(s.Socket)
			if err != nil {
				return
			}
			c.Close()
			time.Sleep(100 * time.Millisecond)
		}
		return
	}
	select {
	case <-exited:
	case <-time.After(stopWait):
		s.Logf("network protection did not stop within %s", stopWait)
	}
}

func exitStatus(err error) string {
	if err == nil {
		return "status 0"
	}
	return err.Error()
}

// sleep waits for d, or returns false if ctx ends first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
