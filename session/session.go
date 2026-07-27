// Package session defines application-owned session and interaction contracts.
package session

import "context"

type Controller interface {
	NewSession(context.Context) error
	Fork(context.Context, string, string) error
	SwitchSession(context.Context, string) error
}

type SessionController = Controller

type Control struct {
	ID      string
	Action  string
	Content string
}

type ControlPoller func(context.Context) ([]Control, error)

type InteractionReply struct {
	Ready, Cancelled bool
	Response         any
}

type InteractionPoller func(context.Context, string) (InteractionReply, error)
