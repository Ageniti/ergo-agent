package agent_test

import (
	core "github.com/ageniti/ergo-agent/agent"
	"github.com/ageniti/ergo-agent/extensions"
	"github.com/ageniti/ergo-agent/extensions/bocha"
	"github.com/ageniti/ergo-agent/message"
	"github.com/ageniti/ergo-agent/provider"
	"github.com/ageniti/ergo-agent/resource"
	"github.com/ageniti/ergo-agent/runner"
	"github.com/ageniti/ergo-agent/session"
	"github.com/ageniti/ergo-agent/tool"
	"testing"
)

func TestNewDefaultLoadsEmbeddedProfiles(t *testing.T) {
	runtime, err := core.NewDefault()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := runtime.Resources.Agent("chief-agent")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "chief-agent" || profile.Role != resource.AgentRoleMain {
		t.Fatalf("unexpected embedded profile: %+v", profile)
	}
	researcher, err := runtime.Resources.Agent("web-researcher")
	if err != nil {
		t.Fatal(err)
	}
	if researcher.Role != resource.AgentRoleMeta || len(researcher.Tools) != 1 || researcher.Tools[0] != "web_search" {
		t.Fatalf("unexpected web researcher profile: %+v", researcher)
	}
}

// Compile-time assignments protect the additive SDK packages from drifting
// away from the original agent package contracts.
var (
	_ core.Message                                     = message.Message{}
	_ message.Message                                  = core.Message{}
	_ core.ToolDefinition                              = tool.Definition{}
	_ tool.Definition                                  = core.ToolDefinition{}
	_ core.Completion                                  = provider.Completion{}
	_ provider.Completion                              = core.Completion{}
	_ core.Resources                                   = resource.Resources{}
	_ resource.Resources                               = core.Resources{}
	_ core.Extension                                   = extensions.Extension{}
	_ extensions.Extension                             = core.Extension{}
	_ core.RunOptions                                  = runner.Options{}
	_ runner.Options                                   = core.RunOptions{}
	_ core.SessionController                           = (session.Controller)(nil)
	_ session.Controller                               = (core.SessionController)(nil)
	_ func(string) *core.Runtime                       = runner.New
	_ func() (*core.Runtime, error)                    = runner.NewDefault
	_ func() (*core.Runtime, error)                    = core.NewDefault
	_ func(provider.Factory) *provider.Registry        = provider.NewRegistry
	_ func(bocha.Config) (extensions.Extension, error) = bocha.New
)
