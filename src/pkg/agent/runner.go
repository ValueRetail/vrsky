package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
)

// ErrRevoked stops the agent: VRSky revoked this machine's credential, and no
// amount of retrying will bring it back. It must be registered again.
var ErrRevoked = errors.New("this agent was revoked in VRSky — register the machine again to reconnect")

// maxConcurrentWrites bounds deliveries written at once.
const maxConcurrentWrites = 4

// Runner is a running agent.
type Runner struct {
	cfg    *Config
	cred   *Credential
	client *Client
	state  *State
	log    *slog.Logger

	watchers map[string]*watcherHandle // folder name → running watcher

	inFlightMu sync.Mutex
	inFlight   map[string]bool // delivery IDs being written
	writeSlots chan struct{}
	wg         sync.WaitGroup

	// sleep is replaced in tests.
	sleep func(ctx context.Context, d time.Duration) bool
}

type watcherHandle struct {
	w      *dirWatcher
	cancel context.CancelFunc
	done   chan struct{}
}

// NewRunner prepares an agent from its config and stored credential.
func NewRunner(cfg *Config, cred *Credential, log *slog.Logger) (*Runner, error) {
	if cred.Revoked {
		return nil, ErrRevoked
	}
	client, err := NewClient(cred.ServerURL, cred.Credential)
	if err != nil {
		return nil, err
	}
	state, err := LoadState(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	return &Runner{
		cfg: cfg, cred: cred, client: client, state: state,
		log:        log.With("agent", cred.Name),
		watchers:   map[string]*watcherHandle{},
		inFlight:   map[string]bool{},
		writeSlots: make(chan struct{}, maxConcurrentWrites),
		sleep:      sleepCtx,
	}, nil
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Run polls VRSky until ctx ends or the credential is revoked. It never exits
// on a network or server error: an agent on a till must survive the VRSky side
// restarting, the network dropping, and the machine waking from sleep.
func (r *Runner) Run(ctx context.Context) error {
	r.log.Info("Agent starting", "server", r.cred.ServerURL, "version", Version,
		"folders", len(r.cfg.Directories))
	defer r.stopAllWatchers()
	defer r.wg.Wait()

	version := ""
	announced := false
	failures := 0
	for ctx.Err() == nil {
		if !announced {
			if err := r.announce(ctx); err != nil {
				if wait, stop := r.onError(err, &failures); stop != nil {
					return stop
				} else if !r.sleep(ctx, wait) {
					break
				}
				continue
			}
			announced = true
		}

		work, err := r.client.Work(ctx, int(agentproto.PollHoldMax/time.Second), version)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			wait, stop := r.onError(err, &failures)
			if stop != nil {
				return stop
			}
			// Announce again once back: the gateway may have restarted, or
			// the machine's folders changed while it was unreachable.
			announced = false
			if !r.sleep(ctx, wait) {
				break
			}
			continue
		}
		if failures > 0 {
			r.log.Info("Connected to VRSky again", "after_failures", failures)
		}
		failures = 0

		if work.WatchesVersion != version {
			r.reconcileWatches(ctx, work.Watches)
			version = work.WatchesVersion
		}
		for _, d := range work.Deliveries {
			r.startDelivery(ctx, d)
		}
		if work.NextPollMS > 0 && !r.sleep(ctx, time.Duration(work.NextPollMS)*time.Millisecond) {
			break
		}
	}
	r.log.Info("Agent stopping")
	return nil
}

func (r *Runner) announce(ctx context.Context) error {
	host, _ := os.Hostname()
	actx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return r.client.Announce(actx, agentproto.AnnounceRequest{
		Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH, Version: Version,
		Directories: r.cfg.Announced(),
	})
}

// onError decides what a failed call means: stop for good (revoked), or wait
// how long before trying again.
func (r *Runner) onError(err error, failures *int) (wait time.Duration, stop error) {
	*failures++
	var ae *APIError
	switch {
	case IsRevoked(err):
		r.log.Error("VRSky revoked this agent; stopping", "error", err)
		r.cred.Revoked = true
		if serr := SaveCredential(r.cfg.DataDir, r.cred); serr != nil {
			r.log.Error("Could not record the revocation", "error", serr)
		}
		return 0, ErrRevoked
	case errors.As(err, &ae) && ae.Status == http.StatusUnauthorized:
		// Not revoked, just unknown: a restored database, or a wrong server.
		// Keep trying, slowly — it may come back.
		r.log.Error("VRSky does not recognise this agent's credential; retrying in 5 minutes", "error", err)
		return 5 * time.Minute, nil
	case errors.As(err, &ae) && ae.Status == http.StatusUpgradeRequired:
		r.log.Error("VRSky needs a newer agent; retrying in 10 minutes", "error", err)
		return 10 * time.Minute, nil
	}
	wait = backoff(*failures)
	if *failures == 1 || *failures%10 == 0 {
		r.log.Warn("Cannot reach VRSky; retrying", "error", err, "attempt", *failures, "wait", wait.Round(time.Millisecond))
	}
	return wait, nil
}

// backoff is exponential from 1 s to 60 s with full jitter, so a fleet of
// agents coming back after an outage does not arrive in lockstep.
func backoff(failures int) time.Duration {
	ceiling := time.Second << min(failures-1, 6)
	if ceiling > time.Minute {
		ceiling = time.Minute
	}
	return time.Duration(rand.Int64N(int64(ceiling))) + 100*time.Millisecond
}

// reconcileWatches starts, updates and stops folder watchers to match the
// watch set VRSky sent. Unknown folders are logged and skipped: VRSky only
// offers folders this agent announced, so one it does not know means the
// config changed and the pipeline needs a different folder.
func (r *Runner) reconcileWatches(ctx context.Context, watches []agentproto.Watch) {
	want := map[string]map[string]string{} // folder → connection → after
	for _, w := range watches {
		if w.Op != "" && w.Op != agentproto.OpWatchDir {
			r.log.Warn("Ignoring a watch of an unknown kind; is this agent out of date?", "op", w.Op)
			continue
		}
		dir, ok := r.cfg.Directories[w.Directory]
		if !ok || dir.Mode != agentproto.ModeRead {
			r.log.Error("A pipeline asks for a folder this agent has no read folder for", "folder", w.Directory, "connection_id", w.ConnectionID)
			continue
		}
		if want[w.Directory] == nil {
			want[w.Directory] = map[string]string{}
		}
		want[w.Directory][w.ConnectionID] = w.After
	}
	for name, h := range r.watchers {
		if _, ok := want[name]; !ok {
			h.cancel()
			<-h.done
			delete(r.watchers, name)
			r.log.Info("Stopped watching", "folder", name)
		}
	}
	for name, conns := range want {
		h := r.watchers[name]
		if h == nil {
			dir := r.cfg.Directories[name]
			wctx, cancel := context.WithCancel(ctx)
			h = &watcherHandle{
				w:      newDirWatcher(name, dir, time.Duration(r.cfg.PollIntervalSeconds)*time.Second, r.client, r.state, r.log),
				cancel: cancel, done: make(chan struct{}),
			}
			r.watchers[name] = h
			h.w.setConnections(conns)
			go func() { defer close(h.done); h.w.run(wctx) }()
			r.log.Info("Watching", "folder", name, "path", dir.Path, "pipelines", len(conns))
			continue
		}
		h.w.setConnections(conns)
	}
}

func (r *Runner) stopAllWatchers() {
	for name, h := range r.watchers {
		h.cancel()
		<-h.done
		delete(r.watchers, name)
	}
}

// startDelivery writes a delivery in the background, unless it is already
// being written — a large file can outlast its lease, and the gateway then
// offers it again.
func (r *Runner) startDelivery(ctx context.Context, d agentproto.Delivery) {
	if d.Op != "" && d.Op != agentproto.OpWriteFile {
		r.log.Warn("Ignoring a delivery of an unknown kind; is this agent out of date?", "op", d.Op)
		return
	}
	r.inFlightMu.Lock()
	if r.inFlight[d.ID] {
		r.inFlightMu.Unlock()
		return
	}
	r.inFlight[d.ID] = true
	r.inFlightMu.Unlock()

	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer func() {
			r.inFlightMu.Lock()
			delete(r.inFlight, d.ID)
			r.inFlightMu.Unlock()
		}()
		select {
		case r.writeSlots <- struct{}{}:
			defer func() { <-r.writeSlots }()
		case <-ctx.Done():
			return
		}
		r.deliver(ctx, d)
	}()
}

func (r *Runner) deliver(ctx context.Context, d agentproto.Delivery) {
	log := r.log.With("delivery_id", d.ID, "folder", d.Directory, "file", d.Filename)
	var body io.Reader
	if d.BodyURL != "" {
		rc, err := r.client.Body(ctx, d.BodyURL)
		if err != nil {
			log.Warn("Could not fetch the file from VRSky; it will be offered again", "error", err)
			return // no ack: the lease expires and the gateway re-offers it
		}
		defer rc.Close()
		body = rc
	} else {
		raw, err := base64.StdEncoding.DecodeString(d.InlineBase64)
		if err != nil {
			r.ack(ctx, log, d.ID, errors.New("undecodable inline body"))
			return
		}
		body = strings.NewReader(string(raw))
	}

	path, err := WriteFile(r.cfg, d.Directory, d.Filename, d.ID, d.Checksum, body)
	if err != nil {
		if ctx.Err() != nil {
			return // shutting down mid-write: no ack, it will be offered again
		}
		log.Error("Could not write the file", "error", err)
		r.ack(ctx, log, d.ID, err)
		return
	}
	log.Info("Wrote file", "path", path, "bytes", d.Size)
	r.ack(ctx, log, d.ID, nil)
}

func (r *Runner) ack(ctx context.Context, log *slog.Logger, id string, writeErr error) {
	for attempt := 1; attempt <= 5; attempt++ {
		err := r.client.Ack(ctx, id, writeErr)
		if err == nil {
			return
		}
		var ae *APIError
		if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
			// The gateway no longer holds it (it restarted, or the lease
			// ran out and another attempt is under way). It will be
			// offered again; writing it again replaces the file atomically.
			log.Warn("VRSky no longer holds this delivery; it will be offered again")
			return
		}
		if !r.sleep(ctx, backoff(attempt)) {
			return
		}
	}
	log.Warn("Could not confirm the delivery to VRSky; it will be offered again")
}
