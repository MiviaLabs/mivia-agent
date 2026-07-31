package coordinator

import "fmt"

import "github.com/MiviaLabs/mivia-agent/internal/subagents"

// validateTasks validates a task DAG. Returns nil if valid.
func (c *coordinator) validateTasks(tasks []subagents.Task) error {
	if len(tasks) == 0 {
		return fmt.Errorf("empty task list")
	}
	if c.pool != nil && c.pool.MaxFanout() > 0 && len(tasks) > c.pool.MaxFanout() {
		return fmt.Errorf("task count exceeds fan-out limit")
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
	indegree := make(map[string]int, len(deps))
	children := make(map[string][]string, len(deps))
	depth := make(map[string]int, len(deps))
	queue := make([]string, 0, len(deps))
	for id, dependencies := range deps {
		indegree[id] = len(dependencies)
		if len(dependencies) == 0 {
			queue = append(queue, id)
		}
		for _, dep := range dependencies {
			children[dep] = append(children[dep], id)
		}
	}
	processed := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		processed++
		for _, child := range children[id] {
			if depth[child] < depth[id]+1 {
				depth[child] = depth[id] + 1
			}
			if c.pool != nil && c.pool.MaxDepth() > 0 && depth[child] > c.pool.MaxDepth() {
				return fmt.Errorf("dependency depth exceeds limit")
			}
			indegree[child]--
			if indegree[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	if processed != len(deps) {
		return fmt.Errorf("dependency cycle detected")
	}
	return nil
}
