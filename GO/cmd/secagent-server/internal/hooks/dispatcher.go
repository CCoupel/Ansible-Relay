package hooks

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"secagent-server/cmd/secagent-server/internal/storage"
)

// ── Job ───────────────────────────────────────────────────────────────────────

type dispatchJob struct {
	event      string
	hostname   string
	status     string
	enrolledAt string // only set for host.new
	timestamp  string // RFC3339 — captured at Dispatch time
}

// ── ActionLogger interface ─────────────────────────────────────────────────────

// ActionLogger is the subset of *storage.Store used to persist execution results.
type ActionLogger interface {
	CreateActionLog(ctx context.Context, entry storage.ActionLogEntry) error
}

// buildVars constructs the template variable map from a dispatch job's fields.
func buildVars(event, hostname, status, timestamp, enrolledAt string) map[string]string {
	return map[string]string{
		"event":       event,
		"hostname":    hostname,
		"status":      status,
		"timestamp":   timestamp,
		"enrolled_at": enrolledAt,
	}
}

// ── Dispatcher ────────────────────────────────────────────────────────────────

// Dispatcher receives lifecycle events and executes all configured hooks asynchronously.
type Dispatcher struct {
	mu     sync.RWMutex
	config *HooksConfig

	queue chan dispatchJob
	store ActionLogger

	webhookExec *WebhookExecutor
	shellExec   *ShellExecutor
	fileExec    *FileExecutor
	apiExec     *APIExecutor
}

// GlobalDispatcher is the server-wide singleton injected from main.go.
var GlobalDispatcher *Dispatcher

// NewDispatcher creates a Dispatcher with a buffered queue of bufSize events.
func NewDispatcher(store ActionLogger, bufSize int) *Dispatcher {
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse // do not follow 3xx
		},
	}
	return &Dispatcher{
		queue:       make(chan dispatchJob, bufSize),
		store:       store,
		webhookExec: &WebhookExecutor{client: client},
		shellExec:   &ShellExecutor{},
		fileExec:    &FileExecutor{},
		apiExec:     &APIExecutor{client: client},
	}
}

// SetConfig replaces the active hooks configuration.
// Thread-safe; called from main.go at startup and on SIGHUP.
func (d *Dispatcher) SetConfig(cfg *HooksConfig) {
	d.mu.Lock()
	d.config = cfg
	d.mu.Unlock()

	if cfg == nil {
		log.Println("[HOOKS] Config cleared — 0 hooks active")
	} else {
		log.Printf("[HOOKS] Config loaded — %d hook(s) active", len(cfg.Hooks))
	}
}

// Start launches the background goroutine that drains the event queue.
// Returns immediately; the goroutine exits when ctx is cancelled.
func (d *Dispatcher) Start(ctx context.Context) {
	go func() {
		log.Println("[HOOKS] Dispatcher started")
		for {
			select {
			case <-ctx.Done():
				log.Printf("[HOOKS] Dispatcher stopped: %v", ctx.Err())
				return
			case job, ok := <-d.queue:
				if !ok {
					return
				}
				d.processJob(ctx, job)
			}
		}
	}()
}

// Dispatch enqueues an event for asynchronous hook execution.
// Never blocks: events are silently dropped when the queue is full.
// enrolledAt is only non-empty for host.new events.
func (d *Dispatcher) Dispatch(event, hostname, status, enrolledAt string) {
	job := dispatchJob{
		event:      event,
		hostname:   hostname,
		status:     status,
		enrolledAt: enrolledAt,
		timestamp:  time.Now().UTC().Format(time.RFC3339),
	}
	select {
	case d.queue <- job:
	default:
		log.Printf("[WARN] hooks queue full, dropping event=%s hostname=%s", event, hostname)
	}
}

// processJob finds the matching HookDef and launches one goroutine per action.
func (d *Dispatcher) processJob(ctx context.Context, job dispatchJob) {
	d.mu.RLock()
	cfg := d.config
	d.mu.RUnlock()

	if cfg == nil {
		return
	}

	vars := buildVars(job.event, job.hostname, job.status, job.timestamp, job.enrolledAt)

	for _, hookDef := range cfg.Hooks {
		if hookDef.Event != job.event {
			continue
		}
		for idx, action := range hookDef.Actions {
			action := action // capture for goroutine
			idx := idx
			go d.executeAction(ctx, job, action, idx, vars)
		}
		return // first matching HookDef wins
	}
}

// executeAction runs one action and logs the result to action_log.
func (d *Dispatcher) executeAction(ctx context.Context, job dispatchJob, action ActionDef, idx int, vars map[string]string) {
	var ex Executor
	switch action.Type {
	case "webhook":
		ex = d.webhookExec
	case "shell":
		ex = d.shellExec
	case "file":
		ex = d.fileExec
	case "api":
		ex = d.apiExec
	default:
		log.Printf("[WARN] hooks: unknown action type %q for event %s hostname=%s", action.Type, job.event, job.hostname)
		return
	}

	success, errMsg, durationMs := ex.Execute(ctx, action, vars)

	snapshot, _ := json.Marshal(action)
	entry := storage.ActionLogEntry{
		ID:             uuid.New().String(),
		Event:          job.event,
		Hostname:       job.hostname,
		ActionType:     action.Type,
		ActionIndex:    idx,
		ConfigSnapshot: string(snapshot),
		Success:        success,
		Error:          errMsg,
		DurationMs:     durationMs,
		ExecutedAt:     time.Now().UTC(),
	}

	if d.store != nil {
		if err := d.store.CreateActionLog(ctx, entry); err != nil {
			log.Printf("[WARN] hooks: CreateActionLog: %v", err)
		}
	}

	if success {
		log.Printf("[HOOKS] %s %s action[%d] type=%s OK duration=%dms", job.event, job.hostname, idx, action.Type, durationMs)
	} else {
		log.Printf("[HOOKS] %s %s action[%d] type=%s FAIL: %s duration=%dms", job.event, job.hostname, idx, action.Type, errMsg, durationMs)
	}
}
