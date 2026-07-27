package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ageniti/ergo-agent/resource"
)

func main() {
	root := flag.String("root", ".", "source Agent resource root")
	entry := flag.String("agent", "", "entry Agent name")
	output := flag.String("output", "", "output package directory")
	name := flag.String("name", "", "npm package name (default @ageniti/<agent>)")
	version := flag.String("version", "0.1.0", "package version")
	license := flag.String("license", "", "package license override (default: inherit source-root LICENSE)")
	flag.Parse()
	if *entry == "" {
		fmt.Fprintln(os.Stderr, "-agent is required")
		os.Exit(2)
	}
	if *output == "" {
		*output = filepath.Join("dist", *entry)
	}
	result, err := (resource.Resources{Root: *root}).BuildAgentPackage(*output, resource.AgentPackageBuildOptions{
		EntryAgent:  *entry,
		PackageName: *name,
		Version:     *version,
		License:     *license,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	data, _ := json.MarshalIndent(result, "", "  ")
	fmt.Println(string(data))
}
