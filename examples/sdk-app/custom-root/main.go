package main

import (
	"context"
	"fmt"
	"os"

	ergoruntime "github.com/ageniti/ergo-agent/runtime"
)

func main() {
	resourceRoot := os.Getenv("AGENT_RESOURCE_ROOT")
	if resourceRoot == "" {
		resourceRoot = "examples/sdk-app/custom-root/resources"
	}
	runtime := ergoruntime.New(resourceRoot)
	err := runtime.RunWithOptions(context.Background(), map[string]any{
		"agentId":  "only-meta",
		"prompt":   "Inspect this repository and summarize its architecture.",
		"cwd":      ".",
		"provider": "openai",
		"model":    "gpt-5",
	}, ergoruntime.RunOptions{}, func(event ergoruntime.Event) error {
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
