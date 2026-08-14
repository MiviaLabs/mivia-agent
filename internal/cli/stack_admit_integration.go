package cli

// integrationRunInputs builds the admission inputs for the final full-suite
// integration run: it replays the plan run's declared inputs and admits as
// stack_mode=single (running the workflow's own plan+implement steps
// inline), never stack_mode=chunk. chunk_plan's chunk/pr_base/stack_part are
// deliberately absent: stack_mode=chunk REQUIRES stack_part present
// (validateStackingReservedInputs), and the integration run has none - a bug
// an adversarial audit found: chunkRunInputs forced stack_mode=chunk here
// with an always-empty stack_part, so every stack's integration run failed
// admission the moment every chunk merged.
func integrationRunInputs(planInputs map[string]string, prBase string) (map[string]any, map[string]string) {
	inputs := make(map[string]any, len(planInputs)+2)
	snapshot := make(map[string]string, len(planInputs)+2)
	for k, v := range planInputs {
		inputs[k] = v
		snapshot[k] = v
	}
	// stack_mode=single forbids chunk_plan (validateStackingReservedInputs),
	// and a plan run admits with one (the implicit-plan path never checks
	// it), so the replay must strip it instead of carrying it over.
	delete(inputs, "chunk_plan")
	delete(snapshot, "chunk_plan")
	inputs["stack_mode"] = "single"
	snapshot["stack_mode"] = "single"
	if prBase != "" {
		inputs["pr_base"] = prBase
		snapshot["pr_base"] = prBase
	}
	return inputs, snapshot
}
