package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type ProjectType string

const (
	ProjectTypeNode    ProjectType = "node"
	ProjectTypeGo      ProjectType = "go"
	ProjectTypeRuby    ProjectType = "ruby"
	ProjectTypePython  ProjectType = "python"
	ProjectTypeRust    ProjectType = "rust"
	ProjectTypeUnknown ProjectType = "unknown"
)

type DetectedProject struct {
	Type     ProjectType
	Commands CommandsConfig
}

func DetectProject(dir string) *DetectedProject {
	if p := detectNode(dir); p != nil {
		return p
	}
	if p := detectGo(dir); p != nil {
		return p
	}
	if p := detectRuby(dir); p != nil {
		return p
	}
	if p := detectPython(dir); p != nil {
		return p
	}
	if p := detectRust(dir); p != nil {
		return p
	}

	return &DetectedProject{
		Type: ProjectTypeUnknown,
	}
}

func detectNode(dir string) *DetectedProject {
	pkgPath := filepath.Join(dir, "package.json")
	data, err := os.ReadFile(pkgPath)
	if err != nil {
		return nil
	}

	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil
	}

	p := &DetectedProject{
		Type: ProjectTypeNode,
	}

	if _, ok := pkg.Scripts["test"]; ok {
		p.Commands.Test = "npm test"
	}
	if _, ok := pkg.Scripts["lint"]; ok {
		p.Commands.Lint = "npm run lint"
	} else if _, ok := pkg.Scripts["lint:fix"]; ok {
		p.Commands.Lint = "npm run lint:fix"
	}
	if _, ok := pkg.Scripts["build"]; ok {
		p.Commands.Build = "npm run build"
	}

	if _, err := os.Stat(filepath.Join(dir, "bun.lockb")); err == nil {
		if p.Commands.Test != "" {
			p.Commands.Test = "bun test"
		}
		if p.Commands.Lint != "" {
			p.Commands.Lint = "bun run lint"
		}
		if p.Commands.Build != "" {
			p.Commands.Build = "bun run build"
		}
	}

	return p
}

func detectGo(dir string) *DetectedProject {
	modPath := filepath.Join(dir, "go.mod")
	if _, err := os.Stat(modPath); err != nil {
		return nil
	}

	return &DetectedProject{
		Type: ProjectTypeGo,
		Commands: CommandsConfig{
			Test: "go test ./...",
			Lint: "golangci-lint run --fix",
		},
	}
}

func detectRuby(dir string) *DetectedProject {
	gemfilePath := filepath.Join(dir, "Gemfile")
	if _, err := os.Stat(gemfilePath); err != nil {
		return nil
	}

	p := &DetectedProject{
		Type: ProjectTypeRuby,
	}

	if _, err := os.Stat(filepath.Join(dir, "bin", "rails")); err == nil {
		p.Commands.Test = "bin/rails test"
		p.Commands.Lint = "bundle exec rubocop -A"
	} else if _, err := os.Stat(filepath.Join(dir, "spec")); err == nil {
		p.Commands.Test = "bundle exec rspec"
		p.Commands.Lint = "bundle exec rubocop -A"
	} else {
		p.Commands.Test = "bundle exec rake test"
		p.Commands.Lint = "bundle exec rubocop -A"
	}

	return p
}

func detectPython(dir string) *DetectedProject {
	for _, f := range []string{"pyproject.toml", "setup.py", "requirements.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err == nil {
			p := &DetectedProject{
				Type: ProjectTypePython,
				Commands: CommandsConfig{
					Test: "pytest",
					Lint: "ruff check --fix .",
				},
			}
			return p
		}
	}
	return nil
}

func detectRust(dir string) *DetectedProject {
	cargoPath := filepath.Join(dir, "Cargo.toml")
	if _, err := os.Stat(cargoPath); err != nil {
		return nil
	}

	return &DetectedProject{
		Type: ProjectTypeRust,
		Commands: CommandsConfig{
			Test:  "cargo test",
			Lint:  "cargo clippy --fix --allow-dirty",
			Build: "cargo build",
		},
	}
}
