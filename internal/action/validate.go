package action

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Bakaface/sakusen/internal/config"
)

type ValidateArgs struct {
	Path string // empty == cwd/.sakusen.yml
}

func (a ValidateArgs) Validate() error {
	// Resolution happens in RunValidate so a bare "validate" works in the CLI.
	return nil
}

// RunValidate resolves the .sakusen.yml path and runs config.Diagnose. Unlike
// every other action this verb does no client call — it goes straight at the
// local filesystem so it works without a running daemon.
func RunValidate(ctx Ctx, args ValidateArgs) (Result, error) {
	path, err := resolveConfigPath(args.Path)
	if err != nil {
		return Result{}, err
	}
	diagnostics, err := config.Diagnose(path)
	if err != nil {
		return Result{}, fmt.Errorf("%s: %w", path, err)
	}
	var b strings.Builder
	for _, d := range diagnostics {
		fmt.Fprintf(&b, "%s: %s: %s\n", path, d.Severity, d.Message)
	}
	fmt.Fprintf(&b, "%s is valid", path)
	return Result{Message: b.String()}, nil
}

func resolveConfigPath(p string) (string, error) {
	if p != "" {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("config file not found: %s", abs)
		}
		return abs, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(cwd, ".sakusen.yml")
	if _, err := os.Stat(candidate); err != nil {
		return "", fmt.Errorf("no .sakusen.yml found in %s — pass a path explicitly", cwd)
	}
	return candidate, nil
}

func init() {
	Registry["validate"] = Action{
		ID:   "validate",
		Help: "Validate a .sakusen.yml configuration file",
		Run: func(ctx Ctx, a Args) (Result, error) {
			return RunValidate(ctx, a.(ValidateArgs))
		},
		Parse: func(raw string) (Args, error) {
			return ValidateArgs{Path: strings.TrimSpace(raw)}, nil
		},
	}
}
