// Package runner provides a focused entry point for executing agents.
//
// Runtime is an alias, not a wrapper. Calls through runner.Runtime and
// agent.Runtime therefore use the same implementation, methods, and state.
package runner

import (
	agentassets "github.com/ageniti/ergo-agent"
	engine "github.com/ageniti/ergo-agent/internal/engine"
)

type (
	Runtime           = engine.Runtime
	RunOptions        = engine.RunOptions
	Command           = engine.Command
	Event             = engine.Event
	EventSink         = engine.EventSink
	Control           = engine.Control
	ControlPoller     = engine.ControlPoller
	InteractionReply  = engine.InteractionReply
	InteractionPoller = engine.InteractionPoller

	Options = RunOptions
)

const (
	RuntimeVersion      = engine.RuntimeVersion
	PromptBundleVersion = engine.PromptBundleVersion
)

func New(root string) *Runtime {
	return engine.New(root)
}

// NewDefault constructs a Runtime using the SDK's embedded default resources.
func NewDefault() (*Runtime, error) {
	root, err := agentassets.DefaultRoot()
	if err != nil {
		return nil, err
	}
	return engine.New(root), nil
}
