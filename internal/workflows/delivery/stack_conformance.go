package delivery

// The stack-driver conformance catalog: the one list of behavioural
// scenarios every stack drive loop must satisfy, with the durable end state
// each scenario must produce. Both drivers (internal/cli/chat and
// internal/workflows/localengine) range over StackConformanceScenarios in
// their own tests; a scenario added here without a wired runner fails that
// driver's conformance test on the unhandled row. This is the structural
// anti-drift gate for the sibling-implementation defect class (see
// .agents/memories/sibling-implementations-drift.md): contracts proven by a
// running table, never by a prose sweep.

// StackConformanceOwner names a driver that must satisfy a scenario.
type StackConformanceOwner string

const (
	// OwnerDriverCLI is the session/CLI drive loop (internal/cli/chat).
	OwnerDriverCLI StackConformanceOwner = "cli-driver"
	// OwnerDriverEngine is the in-process engine drive loop
	// (internal/workflows/localengine).
	OwnerDriverEngine StackConformanceOwner = "engine-driver"
	// OwnerCLIOnly marks a scenario only the CLI driver can satisfy, because
	// it exercises an action the engine must never perform (squash merge,
	// overlap guard on merge). Engine rows for these are forbidden by the
	// no-merge-authority contract.
	OwnerCLIOnly StackConformanceOwner = "cli-only"
)

// ConformanceScenarioChunk is one chunk of a conformance scenario: its plan
// entry and the events the driver fixture must model for it.
type ConformanceScenarioChunk struct {
	ID    string
	Deps  []string
	Files []string
	// Events lists the durable facts the fixture seeds for the chunk, in
	// driver-agnostic vocabulary. Known values: "merged" (PR landed),
	// "failed" (attempt budget exhausted), "canceled" (run canceled),
	// "overlap" (its changed files overlap another chunk's), "pruned-branch"
	// (merged with the head branch deleted afterwards).
	Events []string
}

// StackConformanceScenario is one row of the shared catalog.
type StackConformanceScenario struct {
	Name        string
	Summary     string
	MergePolicy string
	Owners      []StackConformanceOwner
	Chunks      []ConformanceScenarioChunk
	// Expect maps chunk id to the required final ledger task status. Every
	// chunk must be covered; drivers assert these exact end states.
	Expect map[string]string
}

// StackConformanceScenarios returns the single ordered scenario catalog. The
// slice is built fresh per call; callers must not mutate it.
func StackConformanceScenarios() []StackConformanceScenario {
	return []StackConformanceScenario{
		{
			Name:        "three-chunk-wave-merged",
			Summary:     "3 chunks, two topological waves, every chunk PR lands; both drivers must reach the same all-merged ledger state.",
			MergePolicy: "auto",
			Owners:      []StackConformanceOwner{OwnerDriverCLI, OwnerDriverEngine},
			Chunks: []ConformanceScenarioChunk{
				{ID: "c1", Files: []string{"a.go"}, Events: []string{"merged"}},
				{ID: "c2", Files: []string{"b.go"}, Events: []string{"merged"}},
				{ID: "c3", Deps: []string{"c1", "c2"}, Files: []string{"c.go"}, Events: []string{"merged"}},
			},
			Expect: map[string]string{
				"c1": StatusMerged, "c2": StatusMerged, "c3": StatusMerged,
			},
		},
		{
			Name:        "chunk-failed-attempt-budget",
			Summary:     "One chunk exhausts its attempt budget; it must end failed while an independent chunk still completes.",
			MergePolicy: "auto",
			Owners:      []StackConformanceOwner{OwnerDriverCLI, OwnerDriverEngine},
			Chunks: []ConformanceScenarioChunk{
				{ID: "c1", Files: []string{"a.go"}, Events: []string{"merged"}},
				{ID: "c2", Files: []string{"b.go"}, Events: []string{"failed"}},
			},
			Expect: map[string]string{
				"c1": StatusMerged, "c2": StatusFailed,
			},
		},
		{
			Name:        "chunk-canceled",
			Summary:     "One chunk's run is canceled; the canceled chunk must read canceled, not failed or reopened.",
			MergePolicy: "auto",
			Owners:      []StackConformanceOwner{OwnerDriverCLI, OwnerDriverEngine},
			Chunks: []ConformanceScenarioChunk{
				{ID: "c1", Files: []string{"a.go"}, Events: []string{"merged"}},
				{ID: "c2", Files: []string{"b.go"}, Events: []string{"canceled"}},
			},
			Expect: map[string]string{
				"c1": StatusMerged, "c2": StatusCanceled,
			},
		},
		{
			Name:        "overlap-guard",
			Summary:     "Two published chunks changed the same file; the CLI merge path must refuse the overlapping squash merge instead of silently dropping one side. CLI-only: the engine has no merge authority.",
			MergePolicy: "auto",
			Owners:      []StackConformanceOwner{OwnerCLIOnly},
			Chunks: []ConformanceScenarioChunk{
				{ID: "c2", Files: []string{"shared.go"}, Events: []string{"merged"}},
				{ID: "c3", Files: []string{"shared.go"}, Events: []string{"overlap"}},
			},
			Expect: map[string]string{
				"c2": StatusMerged, "c3": StatusPublished,
			},
		},
		{
			Name:        "squash-merged-pruned-branch",
			Summary:     "A chunk PR was squash-merged and its head branch deleted; both drivers must resolve merged from the pushed evidence plus the PR state, without needing FindByHead to resolve the pruned ref.",
			MergePolicy: "auto",
			Owners:      []StackConformanceOwner{OwnerDriverCLI, OwnerDriverEngine},
			Chunks: []ConformanceScenarioChunk{
				{ID: "c1", Files: []string{"a.go"}, Events: []string{"merged", "pruned-branch"}},
			},
			Expect: map[string]string{
				"c1": StatusMerged,
			},
		},
	}
}

// ScenarioOwnedBy reports whether the scenario lists the owner.
func (s StackConformanceScenario) ScenarioOwnedBy(owner StackConformanceOwner) bool {
	for _, o := range s.Owners {
		if o == owner {
			return true
		}
	}
	return false
}
