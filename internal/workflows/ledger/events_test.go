package ledger

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ---- EventID contract ----

func TestEventIDDeterministic(t *testing.T) {
	runID, kind := "wfr-1", eventKindAttemptStarted
	parts := []string{"plan:review", "步骤", ""}
	want := EventID(runID, kind, parts...)
	for i := 0; i < 100; i++ {
		if got := EventID(runID, kind, parts...); got != want {
			t.Fatalf("EventID not deterministic: call %d = %q, want %q", i, got, want)
		}
	}
	if want == "" {
		t.Error("EventID returned an empty ID for a non-empty logical key")
	}
}

func TestEventIDInjectivity(t *testing.T) {
	const (
		runID = "wfr-1"
		kind  = eventKindAttemptStarted
	)

	// Different run IDs must yield different IDs.
	if EventID(runID, kind, "a") == EventID("wfr-2", kind, "a") {
		t.Error("EventID must differ for different run IDs")
	}

	// Different kinds must yield different IDs.
	if EventID(runID, kind, "a") == EventID(runID, eventKindRunCreated, "a") {
		t.Error("EventID must differ for different kinds")
	}

	// Different part sequences are different logical keys. The delimiter
	// must be injective against caller-controlled characters: step IDs,
	// loop names and keys may contain ':', '/', spaces, non-ASCII text, and
	// may be empty. None of these may alias another distinct logical key.
	collisions := []struct {
		name string
		a, b []string
	}{
		{"colon step id vs split parts", []string{"plan:review"}, []string{"plan", "review"}},
		{"colon pair (task example)", []string{"a:b"}, []string{"a", "b"}},
		{"slash vs split parts", []string{"a/b"}, []string{"a", "b"}},
		{"space vs split parts", []string{"a b"}, []string{"a", "b"}},
		{"unicode vs split runes", []string{"步骤"}, []string{"步", "骤"}},
		{"unicode vs ascii part", []string{"步骤"}, []string{"step"}},
		{"empty part vs no parts", []string{""}, []string{}},
		{"empty part vs non-empty part", []string{""}, []string{"x"}},
		{"one part vs two parts", []string{"a"}, []string{"a", "b"}},
	}
	for _, c := range collisions {
		gotA := EventID(runID, kind, c.a...)
		gotB := EventID(runID, kind, c.b...)
		if gotA == gotB {
			t.Errorf("%s: EventID collision between %q and %q (both %q)", c.name, c.a, c.b, gotA)
		}
	}
}

func TestEventIDPrefix(t *testing.T) {
	keys := [][]string{
		{"wfr-1", eventKindRunCreated},
		{"wfr-1", eventKindAttemptStarted},
		{"wfr-1", eventKindAttemptStarted, "plan:review", "步骤", ""},
		{"wfr-1", eventKindAttemptStarted, "a", "b"},
	}
	for _, k := range keys {
		id := EventID(k[0], k[1], k[2:]...)
		if !strings.HasPrefix(id, "wfe:") {
			t.Errorf("EventID(%q, %q, %v) = %q: missing \"wfe:\" prefix", k[0], k[1], k[2:], id)
		}
	}
}

func TestEventIDPartsArePositional(t *testing.T) {
	got := EventID("wfr-1", eventKindAttemptStarted, "a", "b")
	if got == EventID("wfr-1", eventKindAttemptStarted, "b", "a") {
		t.Errorf("EventID parts must be positional (order must matter): %q", got)
	}
}

// ---- Payload marshal/unmarshal round trips ----

// roundTrip asserts that marshal succeeds with non-empty JSON and that the
// corresponding unmarshal reproduces the payload exactly (reflect.DeepEqual).
// time.Time round-trips exactly through JSON (RFC3339Nano), so timestamps
// built with time.Date (UTC, fixed nanoseconds, no monotonic reading) must
// compare equal after the round trip.
func roundTrip[T any](t *testing.T, marshal func(T) ([]byte, error), unmarshal func([]byte) (T, error), in T) {
	t.Helper()
	data, err := marshal(in)
	if err != nil {
		t.Fatalf("marshal returned error for valid payload: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("marshal returned empty JSON for valid payload")
	}
	out, err := unmarshal(data)
	if err != nil {
		t.Fatalf("unmarshal returned error for %s: %v", data, err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("payload did not round-trip\nin:  %#v\nout: %#v", in, out)
	}
}

// ts builds a UTC timestamp with a fixed nanosecond component that round-trips
// exactly through RFC3339Nano JSON encoding (no monotonic clock reading).
func ts(y int, m time.Month, d, h, mi, s, ns int) time.Time {
	return time.Date(y, m, d, h, mi, s, ns, time.UTC)
}

func tsp(t time.Time) *time.Time { return &t }

// roundTripPayload runs one payload round trip under a subtest, dispatching
// on the payload type so callers share a single assertion path.
func roundTripPayload[T any](t *testing.T, name string, p T) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		switch v := any(p).(type) {
		case runCreatedPayload:
			roundTrip(t, marshalRunCreated, unmarshalRunCreated, v)
		case runStatusChangedPayload:
			roundTrip(t, marshalRunStatusChanged, unmarshalRunStatusChanged, v)
		case attemptStartedPayload:
			roundTrip(t, marshalAttemptStarted, unmarshalAttemptStarted, v)
		case attemptCompletedPayload:
			roundTrip(t, marshalAttemptCompleted, unmarshalAttemptCompleted, v)
		case attemptPromptPayload:
			roundTrip(t, marshalAttemptPrompt, unmarshalAttemptPrompt, v)
		case attemptHeartbeatPayload:
			roundTrip(t, marshalAttemptHeartbeat, unmarshalAttemptHeartbeat, v)
		case loopIncrementedPayload:
			roundTrip(t, marshalLoopIncremented, unmarshalLoopIncremented, v)
		case approvalCreatedPayload:
			roundTrip(t, marshalApprovalCreated, unmarshalApprovalCreated, v)
		case approvalResolvedPayload:
			roundTrip(t, marshalApprovalResolved, unmarshalApprovalResolved, v)
		case deliveryUpsertedPayload:
			roundTrip(t, marshalDeliveryUpserted, unmarshalDeliveryUpserted, v)
		default:
			t.Fatalf("roundTripPayload: unsupported payload type %T", p)
		}
	})
}

// TestPayloadRoundTripRunEvents covers the wf_run_created and
// wf_run_status_changed payload round trips.
func TestPayloadRoundTripRunEvents(t *testing.T) {
	roundTripPayload(t, "runCreated", runCreatedPayload{
		Run: RunSnapshot{
			RunID:          "wfr-1",
			WorkflowName:   "test-wf",
			WorkflowDigest: "sha256:aaaa",
			SnapshotDigest: "sha256:bbbb",
			InputDigest:    "sha256:cccc",
			Status:         RunStatusRunning,
			ActiveStepID:   "plan:review",
			BaseRef:        "main",
			BaseCommit:     "c0ffee",
			WorktreeName:   "wt-1",
			Version:        3,
			StartedAt:      ts(2024, time.January, 2, 3, 4, 5, 123456789),
			DeadlineAt:     tsp(ts(2024, time.January, 3, 0, 0, 0, 0)),
			FinishedAt:     tsp(ts(2024, time.January, 4, 1, 2, 3, 987654321)),
		},
		SnapshotJSON: []byte{0x00, 0x01, 0x02, 0xff, 0xfe},
		CreatedAt:    ts(2024, time.January, 2, 3, 4, 5, 123456789),
	})
	roundTripPayload(t, "runStatusChanged", runStatusChangedPayload{
		Status:     RunStatusWaitingApproval,
		Version:    7,
		FinishedAt: tsp(ts(2024, time.February, 1, 2, 3, 4, 500000000)),
		CreatedAt:  ts(2024, time.February, 1, 2, 3, 4, 600000000),
	})
}

// TestPayloadRoundTripAttemptEvents covers the wf_attempt_started and
// wf_attempt_completed payload round trips.
func TestPayloadRoundTripAttemptEvents(t *testing.T) {
	roundTripPayload(t, "attemptStarted", attemptStartedPayload{
		Attempt: StepAttempt{
			AttemptID:        "att-7",
			RunID:            "wfr-1",
			StepID:           "步骤",
			AttemptNo:        2,
			Status:           AttemptStatusRunning,
			CoordinatorRunID: "run-42",
			TaskID:           "task-9",
			OutputRef:        "refs/out/1",
			OutputDigest:     "sha256:dddd",
			ToStepID:         "review",
			TransitionIndex:  1,
			MatchDigest:      "match-1",
			DecisionJSON:     []byte{0x00, 0xde, 0xad, 0xbe, 0xef, 0xff},
			StartedAt:        ts(2024, time.March, 3, 3, 3, 3, 333333333),
			FinishedAt:       tsp(ts(2024, time.March, 3, 3, 3, 3, 400000000)),
			Version:          4,
		},
		CreatedAt: ts(2024, time.March, 3, 3, 3, 3, 444444444),
	})
	roundTripPayload(t, "attemptCompleted", attemptCompletedPayload{
		AttemptID:       "att-7",
		Status:          AttemptStatusSucceeded,
		OutputRef:       "refs/out/1",
		OutputDigest:    "sha256:dddd",
		ToStepID:        "deliver",
		TransitionIndex: 2,
		MatchDigest:     "match-2",
		DecisionJSON:    []byte{0x01, 0x00, 0xff, 0x7f},
		FinishedAt:      ts(2024, time.April, 4, 4, 4, 4, 444444444),
		CreatedAt:       ts(2024, time.April, 4, 4, 4, 4, 555555555),
	})
}

// TestPayloadRoundTripAttemptPrompt covers the wf_attempt_prompt payload
// round trip. The payload carries ONLY attempt identity + a content reference
// (never prompt content), so the round trip is exact for a fixed timestamp.
func TestPayloadRoundTripAttemptPrompt(t *testing.T) {
	roundTripPayload(t, "attemptPrompt", attemptPromptPayload{
		AttemptID: "att-7",
		PromptRef: "refs/prompts/1",
		CreatedAt: ts(2024, time.September, 9, 9, 9, 9, 999999999),
	})
}

// TestPayloadRoundTripAttemptHeartbeat covers the wf_attempt_heartbeat payload
// round trip: attempt identity plus the heartbeat instant.
func TestPayloadRoundTripAttemptHeartbeat(t *testing.T) {
	roundTripPayload(t, "attemptHeartbeat", attemptHeartbeatPayload{
		AttemptID:   "att-7",
		HeartbeatAt: ts(2024, time.October, 10, 10, 10, 10, 101010101),
		CreatedAt:   ts(2024, time.October, 10, 10, 10, 10, 101010101),
	})
}

// TestAttemptHeartbeatValidation verifies marshalAttemptHeartbeat rejects an
// empty attempt id and a zero heartbeat instant, and normalizes a zero
// CreatedAt onto the heartbeat instant so the payload is a deterministic
// function of (AttemptID, HeartbeatAt): a retried append of the same heartbeat
// is byte-identical and dedupes on the event ID instead of conflicting.
func TestAttemptHeartbeatValidation(t *testing.T) {
	if _, err := marshalAttemptHeartbeat(attemptHeartbeatPayload{
		HeartbeatAt: ts(2024, time.October, 10, 10, 10, 10, 0),
	}); err == nil {
		t.Error("marshalAttemptHeartbeat accepted an empty AttemptID")
	}
	if _, err := marshalAttemptHeartbeat(attemptHeartbeatPayload{
		AttemptID: "att-7",
	}); err == nil {
		t.Error("marshalAttemptHeartbeat accepted a zero HeartbeatAt")
	}

	// A zero CreatedAt is normalized onto HeartbeatAt, so the payload is
	// deterministic for one heartbeat instant.
	data, err := marshalAttemptHeartbeat(attemptHeartbeatPayload{AttemptID: "att-7", HeartbeatAt: ts(2024, time.October, 10, 10, 10, 10, 0)})
	if err != nil {
		t.Fatalf("marshalAttemptHeartbeat with zero CreatedAt failed: %v", err)
	}
	out, err := unmarshalAttemptHeartbeat(data)
	if err != nil {
		t.Fatalf("unmarshalAttemptHeartbeat failed: %v", err)
	}
	if !out.CreatedAt.Equal(out.HeartbeatAt) {
		t.Fatalf("normalized CreatedAt = %v, want it to mirror HeartbeatAt %v", out.CreatedAt, out.HeartbeatAt)
	}

	// Two marshal calls for the SAME heartbeat instant are byte-identical
	// (idempotent append input).
	second, err := marshalAttemptHeartbeat(attemptHeartbeatPayload{AttemptID: "att-7", HeartbeatAt: ts(2024, time.October, 10, 10, 10, 10, 0)})
	if err != nil {
		t.Fatalf("marshalAttemptHeartbeat (second call) failed: %v", err)
	}
	if !bytes.Equal(data, second) {
		t.Fatalf("marshalAttemptHeartbeat is not deterministic for one heartbeat instant:\n%q\n%q", data, second)
	}
}

// TestAttemptPromptValidation verifies marshalAttemptPrompt rejects empty
// identity/reference fields and normalizes a zero CreatedAt to the current
// time instead of persisting the zero timestamp.
func TestAttemptPromptValidation(t *testing.T) {
	// Empty AttemptID is rejected even when the reference is present.
	if _, err := marshalAttemptPrompt(attemptPromptPayload{
		PromptRef: "refs/prompts/1",
		CreatedAt: ts(2024, time.September, 9, 9, 9, 9, 999999999),
	}); err == nil {
		t.Error("marshalAttemptPrompt accepted an empty AttemptID")
	}

	// Empty PromptRef is rejected even when the identity is present.
	if _, err := marshalAttemptPrompt(attemptPromptPayload{
		AttemptID: "att-7",
		CreatedAt: ts(2024, time.September, 9, 9, 9, 9, 999999999),
	}); err == nil {
		t.Error("marshalAttemptPrompt accepted an empty PromptRef")
	}

	// A zero CreatedAt is normalized to the current time, never the zero
	// (0001-01-01) timestamp.
	before := time.Now()
	data, err := marshalAttemptPrompt(attemptPromptPayload{AttemptID: "att-7", PromptRef: "refs/prompts/1"})
	if err != nil {
		t.Fatalf("marshalAttemptPrompt with zero CreatedAt failed: %v", err)
	}
	out, err := unmarshalAttemptPrompt(data)
	if err != nil {
		t.Fatalf("unmarshalAttemptPrompt failed: %v", err)
	}
	if out.CreatedAt.IsZero() {
		t.Error("zero CreatedAt was persisted as the zero timestamp")
	}
	if out.CreatedAt.Before(before.Add(-time.Minute)) || out.CreatedAt.After(time.Now().Add(time.Minute)) {
		t.Errorf("normalized CreatedAt = %v, want approximately time.Now()", out.CreatedAt)
	}
}

// TestAttemptPromptPayloadOnlyMetadata proves the wf_attempt_prompt payload
// carries ONLY ref metadata: the struct has no prompt-content field, marshal
// emits exactly the three declared keys, and a prompt body smuggled into the
// JSON is dropped by unmarshal and never re-emitted.
func TestAttemptPromptPayloadOnlyMetadata(t *testing.T) {
	data, err := marshalAttemptPrompt(attemptPromptPayload{
		AttemptID: "att-7",
		PromptRef: "refs/prompts/1",
		CreatedAt: ts(2024, time.September, 9, 9, 9, 9, 999999999),
	})
	if err != nil {
		t.Fatalf("marshalAttemptPrompt failed: %v", err)
	}

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatalf("unmarshal payload as map failed: %v", err)
	}
	want := []string{"attempt_id", "prompt_ref", "created_at"}
	if len(keys) != len(want) {
		t.Fatalf("payload has %d keys, want exactly %d (%v): %s", len(keys), len(want), want, data)
	}
	for _, k := range want {
		if _, ok := keys[k]; !ok {
			t.Errorf("payload missing metadata key %q: %s", k, data)
		}
	}

	// Smuggling attempt: JSON carrying prompt-content fields decodes into the
	// struct (no such fields), so the content is dropped and a re-marshal
	// emits only the metadata keys.
	smuggled := []byte(`{"attempt_id":"att-7","prompt_ref":"refs/prompts/1","created_at":"2024-09-09T09:09:09.999999999Z","prompt":"do not log this","content":"also secret"}`)
	p, err := unmarshalAttemptPrompt(smuggled)
	if err != nil {
		t.Fatalf("unmarshal smuggled payload failed: %v", err)
	}
	re, err := marshalAttemptPrompt(p)
	if err != nil {
		t.Fatalf("re-marshal of unmarshaled payload failed: %v", err)
	}
	var reKeys map[string]json.RawMessage
	if err := json.Unmarshal(re, &reKeys); err != nil {
		t.Fatalf("unmarshal re-marshaled payload as map failed: %v", err)
	}
	for _, extra := range []string{"prompt", "content"} {
		if _, ok := reKeys[extra]; ok {
			t.Errorf("re-marshal emitted smuggled key %q: %s", extra, re)
		}
	}
}

// TestPayloadRoundTripLoopAndApproval covers the wf_loop_incremented,
// wf_approval_created and wf_approval_resolved payload round trips.
func TestPayloadRoundTripLoopAndApproval(t *testing.T) {
	roundTripPayload(t, "loopIncremented", loopIncrementedPayload{
		LoopName:   "retry",
		Iterations: 3,
		CreatedAt:  ts(2024, time.May, 5, 5, 5, 5, 555555555),
	})
	roundTripPayload(t, "approvalCreated", approvalCreatedPayload{
		Approval: ApprovalRecord{
			ApprovalID:   "ap-1",
			RunID:        "wfr-1",
			StepID:       "human-gate",
			Status:       "pending",
			Actor:        "alice",
			Reason:       "please review",
			EvidenceJSON: []byte{0x00, 0x11, 0x22, 0xff},
			CreatedAt:    ts(2024, time.June, 6, 6, 6, 6, 666666666),
			ResolvedAt:   tsp(ts(2024, time.June, 7, 7, 7, 7, 777777777)),
		},
		CreatedAt: ts(2024, time.June, 6, 6, 6, 6, 666666666),
	})
	roundTripPayload(t, "approvalResolved", approvalResolvedPayload{
		ApprovalID: "ap-1",
		Status:     "approved",
		Actor:      "bob",
		Reason:     "looks good",
		ResolvedAt: ts(2024, time.July, 7, 7, 7, 7, 777777777),
		CreatedAt:  ts(2024, time.July, 7, 7, 7, 7, 888888888),
	})
}

// TestPayloadRoundTripDelivery covers the wf_delivery_upserted payload
// round trip.
func TestPayloadRoundTripDelivery(t *testing.T) {
	roundTripPayload(t, "deliveryUpserted", deliveryUpsertedPayload{
		Delivery: DeliveryRecord{
			RunID:          "wfr-1",
			IdempotencyKey: "delivery-1",
			Mode:           "github",
			BaseRef:        "main",
			HeadRef:        "feature/x",
			CommitSHA:      "deadbeef",
			Provider:       "github",
			RemoteID:       "pr-42",
			URL:            "https://example.com/pr/42",
			Status:         "delivery_pending",
			ErrorRef:       "refs/errors/1",
			UpdatedAt:      ts(2024, time.August, 8, 8, 8, 8, 888888888),
		},
		CreatedAt: ts(2024, time.August, 8, 8, 8, 8, 999999999),
	})
}
