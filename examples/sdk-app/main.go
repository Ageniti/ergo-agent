package main

import (
	"context"
	"fmt"
	"os"

	agent "github.com/ageniti/ergo-agent/agent"
	bochaextension "github.com/ageniti/ergo-agent/extensions/bocha"
)

func main() {
	runtime, err := agent.NewDefault()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if extension, enabled, extensionErr := bochaextension.NewFromEnv(); extensionErr != nil {
		fmt.Fprintln(os.Stderr, extensionErr)
		os.Exit(1)
	} else if enabled {
		runtime.RegisterExtension(extension)
		_ = os.Unsetenv("BOCHA_API_KEY")
	}
	err = runtime.RunWithOptions(context.Background(), map[string]any{
		"agentId":  "chief-agent",
		"prompt":   "Inspect this project and summarize its structure.",
		"cwd":      ".",
		"provider": "openai",
		"model":    "gpt-5",
	}, agent.RunOptions{}, func(event agent.Event) error {
		if event.Type == "agent.message_end" || event.Type == "run.completed" {
			fmt.Printf("%s: %v\n", event.Type, event.Payload)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
