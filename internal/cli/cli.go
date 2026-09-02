package cli

import (
	"fmt"
	"io"

	"github.com/monshunter/xgoal/internal/config"
)

const version = "dev"

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printUsage(stdout)
		return 0
	}

	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "xgoal %s\n", version)
		return 0
	case "config":
		return runConfig(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) != 3 || args[0] != "validate" || args[1] != "--file" || args[2] == "" {
		fmt.Fprintln(stderr, "usage: xgoal config validate --file <path>")
		return 2
	}
	cfg, err := config.LoadFile(args[2])
	if err != nil {
		fmt.Fprintf(stderr, "invalid config: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "valid %s %s %q\n", cfg.APIVersion, cfg.Kind, cfg.Metadata.Name)
	return 0
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "xgoal - evidence-closed coding orchestrator")
	fmt.Fprintln(writer, "")
	fmt.Fprintln(writer, "Implemented commands:")
	fmt.Fprintln(writer, "  xgoal version")
	fmt.Fprintln(writer, "  xgoal config validate --file <path>")
}
