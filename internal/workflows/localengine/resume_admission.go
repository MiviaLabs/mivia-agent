package localengine

import "fmt"

func (e *Engine) reserveResume(runID string) (chan struct{}, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.active[runID]; ok {
		return nil, fmt.Errorf("workflow run %q is already executing in this engine; cancel it first", runID)
	}
	if e.interrupting[runID] != 0 {
		return nil, fmt.Errorf("workflow run %q is being interrupted in this engine; wait for it to finish", runID)
	}
	if _, ok := e.resuming[runID]; ok {
		return nil, fmt.Errorf("workflow run %q is already being resumed in this engine", runID)
	}
	if e.resuming == nil {
		e.resuming = make(map[string]chan struct{})
	}
	done := make(chan struct{})
	e.resuming[runID] = done
	return done, nil
}

func (e *Engine) finishResume(runID string, done chan struct{}) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.resuming[runID] == done {
		delete(e.resuming, runID)
		close(done)
	}
}
