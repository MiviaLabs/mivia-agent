package coordinator

import "fmt"

import "github.com/MiviaLabs/mivia-agent/internal/subagents"

// validateTasks validates a task DAG. Returns nil if valid.
func (c *Coordinator) validateTasks(tasks []subagents.Task) error {
	if len(tasks) == 0 {
		return fmt.Errorf("empty task list")
	}
	byID := map[string]bool{}
	for _, t := range tasks {
		if t.ID == "" {
			if len(t.DependsOn) > 0 {
				return fmt.Errorf("anonymous task cannot declare dependencies")
			}
			continue // will be assigned
		}
		if byID[t.ID] {
			return fmt.Errorf("duplicate task id: %s", t.ID)
		}
		byID[t.ID] = true
	}
	// Validate all dependencies exist.
	deps := make(map[string][]string, len(tasks))
	for _, t := range tasks {
		if t.ID == "" {
			continue
		}
		deps[t.ID] = append([]string(nil), t.DependsOn...)
		for _, dep := range t.DependsOn {
			if !byID[dep] {
				return fmt.Errorf("task %q depends on unknown task %q", t.ID, dep)
			}
		}
	}
	visit := make(map[string]uint8, len(deps))
	var visitTask func(string) error
	visitTask = func(id string) error {
		switch visit[id] {
		case 1:
			return fmt.Errorf("dependency cycle involving task %q", id)
		case 2:
			return nil
		}
		visit[id] = 1
		for _, dep := range deps[id] {
			if err := visitTask(dep); err != nil {
				return err
			}
		}
		visit[id] = 2
		return nil
	}
	for id := range deps {
		if err := visitTask(id); err != nil {
			return err
		}
	}
	return nil
}
