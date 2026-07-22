package policy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// Action is a pluggable policy action. Implementations must be side-effecting
// only outside the platform (HTTP calls, email, etc.) — they never call into
// Governance or Audit. Run must be safe to retry.
type Action interface {
	Run(ctx context.Context, ev Event, config json.RawMessage) error
}

// ErrUnknownAction is returned when a policy names an unregistered action type.
var ErrUnknownAction = errors.New("policy: unknown action type")

// ErrActionNotConfigured is returned by interface-only actions with no backend.
var ErrActionNotConfigured = errors.New("policy: action backend not configured")

// Registry maps action types to implementations. It is the pluggability seam:
// operators reference an action type; the executor dispatches through here.
type Registry struct {
	actions map[string]Action
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry { return &Registry{actions: map[string]Action{}} }

// Register adds or replaces an action implementation for a type.
func (r *Registry) Register(actionType string, a Action) { r.actions[actionType] = a }

// Has reports whether an action type is registered.
func (r *Registry) Has(actionType string) bool { _, ok := r.actions[actionType]; return ok }

// Run dispatches to the registered action, or ErrUnknownAction.
func (r *Registry) Run(ctx context.Context, actionType string, ev Event, config json.RawMessage) error {
	a, ok := r.actions[actionType]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownAction, actionType)
	}
	return a.Run(ctx, ev, config)
}

// HTTPDoer is the minimal HTTP client the HTTP action needs (satisfied by
// *http.Client).
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// HTTPAction POSTs the event as JSON to a configured URL. Config: {"url": "..."}.
type HTTPAction struct {
	Doer    HTTPDoer
	Timeout time.Duration
}

// Run implements Action.
func (a HTTPAction) Run(ctx context.Context, ev Event, config json.RawMessage) error {
	var cfg struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(config, &cfg); err != nil {
		return fmt.Errorf("policy http action: bad config: %w", err)
	}
	if cfg.URL == "" {
		return errors.New("policy http action: url is required")
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	timeout := a.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.Doer.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("policy http action: status %d", resp.StatusCode)
	}
	return nil
}

// EmailSender is the pluggable backend for the email action (interface only).
type EmailSender interface {
	Send(ctx context.Context, ev Event, config json.RawMessage) error
}

// EmailAction sends an email via an injected sender. With no sender it is a
// configured-but-inert action (ErrActionNotConfigured).
type EmailAction struct{ Sender EmailSender }

// Run implements Action.
func (a EmailAction) Run(ctx context.Context, ev Event, config json.RawMessage) error {
	if a.Sender == nil {
		return ErrActionNotConfigured
	}
	return a.Sender.Send(ctx, ev, config)
}

// CommandRunner is the pluggable backend for the command action (interface only).
type CommandRunner interface {
	Execute(ctx context.Context, ev Event, config json.RawMessage) error
}

// CommandAction executes a command via an injected runner.
type CommandAction struct{ Runner CommandRunner }

// Run implements Action.
func (a CommandAction) Run(ctx context.Context, ev Event, config json.RawMessage) error {
	if a.Runner == nil {
		return ErrActionNotConfigured
	}
	return a.Runner.Execute(ctx, ev, config)
}

// Notifier is the pluggable backend for internal notifications.
type Notifier interface {
	Notify(ctx context.Context, ev Event, config json.RawMessage) error
}

// NotificationAction publishes an internal notification. With no notifier it logs
// the event (a safe default internal sink).
type NotificationAction struct {
	Notifier Notifier
	Log      *slog.Logger
}

// Run implements Action.
func (a NotificationAction) Run(ctx context.Context, ev Event, config json.RawMessage) error {
	if a.Notifier != nil {
		return a.Notifier.Notify(ctx, ev, config)
	}
	log := a.Log
	if log == nil {
		log = slog.Default()
	}
	log.Info("policy notification", "event_id", ev.EventID, "operation", ev.Operation, "cfg_id", ev.CfgID)
	return nil
}

// DefaultRegistry builds a registry with the built-in actions wired. Email and
// command backends are optional (nil => interface-only / inert).
func DefaultRegistry(doer HTTPDoer, email EmailSender, cmd CommandRunner, notifier Notifier, log *slog.Logger) *Registry {
	r := NewRegistry()
	r.Register("http", HTTPAction{Doer: doer})
	r.Register("email", EmailAction{Sender: email})
	r.Register("command", CommandAction{Runner: cmd})
	r.Register("notification", NotificationAction{Notifier: notifier, Log: log})
	return r
}
