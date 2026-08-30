package director

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rwmyers/agent-director/internal/conf"
)

// Environment variables that select a root and a director. The same names are
// injected into every spawned agent, so an agent's `director report` resolves
// the same root its director is using without being told where it is.
const (
	EnvRoot       = "DIRECTOR_ROOT"
	EnvID         = "DIRECTOR_ID"
	EnvEngagement = "DIRECTOR_ENGAGEMENT"
	EnvToken      = "DIRECTOR_TOKEN"
	EnvTask       = "DIRECTOR_TASK"
	EnvProgress   = "DIRECTOR_PROGRESS"
	// EnvBin is the absolute path of the director binary that spawned the
	// agent, so the callback works even when nothing is installed on PATH.
	EnvBin = "DIRECTOR_BIN"
)

// ProjectDirName is the directory a project keeps its director configuration in.
const ProjectDirName = ".director"

// LayerKind says where a layer came from, for `director where`.
type LayerKind string

const (
	LayerProject LayerKind = "project"
	LayerUser    LayerKind = "user"
)

// Layer is one place configuration can come from.
type Layer struct {
	Kind LayerKind `json:"kind"`
	Path string    `json:"path"`
}

// Roots is the resolved configuration search path.
//
// Primary is the root that owns state: every director registered under it, and
// every engagement those directors hold. That is what makes two projects
// independent — a director in one project cannot see the other's fleet, because
// it is not looking in the same place.
//
// Layers is the ordered search path for workflows and prompts, nearest first,
// so a project can override one workflow without restating the rest.
type Roots struct {
	Primary string  `json:"primary"`
	Layers  []Layer `json:"layers"`
}

// UserRoot is the per-user configuration root.
func UserRoot() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "director")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "director")
}

// ResolveRoots works out which configuration applies.
//
// Order: an explicit path, then DIRECTOR_ROOT, then the nearest .director
// directory at or above the working directory, then the user root. Walking up
// is deliberate — it means a director started in a subdirectory of a project
// still finds that project's workflows, the same way git finds its repository.
func ResolveRoots(explicit, workingDir string) (Roots, error) {
	user := UserRoot()

	primary := explicit
	if primary == "" {
		primary = os.Getenv(EnvRoot)
	}
	if primary == "" {
		if found, ok := findProjectRoot(workingDir); ok {
			primary = found
		}
	}
	if primary == "" {
		primary = user
	}
	if primary == "" {
		return Roots{}, errors.New("cannot determine a configuration root: pass --config or set DIRECTOR_ROOT")
	}

	absolute, err := filepath.Abs(primary)
	if err != nil {
		return Roots{}, err
	}

	roots := Roots{Primary: absolute}
	kind := LayerProject
	if absolute == user {
		kind = LayerUser
	}
	roots.Layers = append(roots.Layers, Layer{Kind: kind, Path: absolute})
	if user != "" && absolute != user {
		roots.Layers = append(roots.Layers, Layer{Kind: LayerUser, Path: user})
	}
	return roots, nil
}

func findProjectRoot(startDir string) (string, bool) {
	dir, err := filepath.Abs(startDir)
	if err != nil {
		return "", false
	}
	for {
		candidate := filepath.Join(dir, ProjectDirName)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

// Config is the core configuration file, director.conf.
//
// Only a small set of keys is understood here. Everything under
// [harness.<name>] is handed to that adapter verbatim and never inspected,
// because validating it would mean knowing about every harness that will ever
// exist.
type Config struct {
	Source    string
	Harness   string
	Harnesses map[string]map[string]string
}

// HarnessConfig returns an adapter's configuration section.
func (c *Config) HarnessConfig(name string) map[string]string {
	if section, ok := c.Harnesses[name]; ok {
		return section
	}
	return map[string]string{}
}

// LoadConfig reads director.conf from a root. A missing file is not an error:
// a root with workflows and no core config is perfectly usable, and demanding
// an empty file would be ceremony.
func LoadConfig(root string) (*Config, error) {
	path := filepath.Join(root, "director.conf")
	config := &Config{Source: path, Harnesses: map[string]map[string]string{}}

	file, err := conf.ParseFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			config.Source = ""
			return config, nil
		}
		return nil, err
	}

	config.Harness = file.Global.Get("harness")
	for _, section := range file.SectionsWithPrefix("harness.") {
		name := strings.TrimPrefix(section.Name, "harness.")
		values := map[string]string{}
		for _, entry := range section.Entries {
			values[entry.Key] = entry.Value
		}
		config.Harnesses[name] = values
	}
	return config, nil
}

// WorkflowsDir is where a root keeps workflow files.
func WorkflowsDir(root string) string { return filepath.Join(root, "workflows") }

// FindWorkflow searches the layers, nearest first, for a named workflow.
func (r Roots) FindWorkflow(name string) (*Workflow, error) {
	for _, layer := range r.Layers {
		path := filepath.Join(WorkflowsDir(layer.Path), name+".conf")
		if _, err := os.Stat(path); err == nil {
			return LoadWorkflow(path)
		}
	}
	available, _ := r.ListWorkflows()
	names := make([]string, len(available))
	for i, workflow := range available {
		names[i] = workflow.Name
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no workflow named %q, and no workflows are defined under %s — run `director init` to create one", name, r.Primary)
	}
	return nil, fmt.Errorf("no workflow named %q (available: %s)", name, strings.Join(names, ", "))
}

// ListWorkflows returns every workflow reachable from the layers. A workflow
// defined in a nearer layer shadows one of the same name further out, which is
// what makes a project override work.
func (r Roots) ListWorkflows() ([]*Workflow, error) {
	seen := map[string]bool{}
	var workflows []*Workflow
	var problems []error

	for _, layer := range r.Layers {
		entries, err := os.ReadDir(WorkflowsDir(layer.Path))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".conf" {
				continue
			}
			name := strings.TrimSuffix(entry.Name(), ".conf")
			if seen[name] {
				continue
			}
			seen[name] = true

			workflow, err := LoadWorkflow(filepath.Join(WorkflowsDir(layer.Path), entry.Name()))
			if err != nil {
				// One broken workflow costs the user that workflow and says
				// so; it must never make the whole command fail, or a typo in
				// an unrelated file becomes an outage.
				problems = append(problems, err)
				continue
			}
			workflows = append(workflows, workflow)
		}
	}

	sort.Slice(workflows, func(i, j int) bool { return workflows[i].Name < workflows[j].Name })
	return workflows, errors.Join(problems...)
}
