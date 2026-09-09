// Package cronschedule wraps github.com/robfig/cron/v3's 5-field
// standard cron parser with an IANA timezone, producing a Spec whose
// Next(after) satisfies mivia-ai-sdk/scheduler.Schedule structurally
// (Next(after time.Time) time.Time). See docs/design/automations.md D10:
// cron is the timing authority, not a display estimate, and Parse stays
// the single entry point so the underlying parser can be swapped later
// without touching any caller.
package cronschedule

import (
	"errors"
	"fmt"
	"time"

	sdkscheduler "github.com/MiviaLabs/mivia-ai-sdk/scheduler"
	"github.com/robfig/cron/v3"
)

// _ pins *Spec to sdkscheduler.Schedule's exact shape at compile time.
// D10's API sketch requires Spec.Next to satisfy that interface so the
// SDK's Scheduler.Add can take a *Spec directly (a later chunk's job,
// not this one's). This import costs no import-layers policy edge:
// .mivia/policy/import-layers.json's edge accounting
// (scripts/check_import_layers.py's compute_edges) only tracks
// module-prefixed internal/* targets, and mivia-ai-sdk/scheduler is an
// external module - so the assertion is free and catches a real SDK
// signature drift (e.g. Next renamed or re-parameterized) at build
// time rather than at the first chunk-6/7 call site. D12 did not list
// an edge for this because none is needed.
var _ sdkscheduler.Schedule = (*Spec)(nil)

// ErrInvalidTZ is returned by Parse when tz does not resolve via
// time.LoadLocation. Wrapped with the offending value and the
// underlying error so callers can distinguish "bad tz" from "bad cron
// expression" (Parse never returns robfig's raw, unclassified error).
var ErrInvalidTZ = errors.New("cronschedule: invalid timezone")

// ErrInvalidExpr is returned by Parse when expr fails robfig's 5-field
// standard-form parse. Wrapped with the offending value and the
// underlying error.
var ErrInvalidExpr = errors.New("cronschedule: invalid cron expression")

// Spec is a parsed 5-field cron expression bound to a resolved IANA
// location. It satisfies the shape mivia-ai-sdk/scheduler.Schedule
// requires (Next(after time.Time) time.Time) without importing that
// package: internal/cronschedule has no policed internal/* edge to the
// SDK scheduler package (it is an external module, not an internal/*
// package, so .mivia/policy/import-layers.json's edge accounting does
// not see it at all - compute_edges only tracks module-prefixed
// internal targets). A real compile-time assertion against the SDK's
// interface type still exists below because it costs no policy edge
// and catches a real regression (e.g. the SDK renaming Next's
// signature) at build time instead of at first call site.
type Spec struct {
	sched cron.Schedule
	loc   *time.Location
}

// Parse parses expr as a 5-field standard cron expression (minute hour
// dom month dow, via cron.ParseStandard) and tz as an IANA timezone
// name (via time.LoadLocation). An empty tz defaults to UTC: this
// matches internal/automation/spec.go's ScheduleSpec.TZ field, which
// documents Cron/TZ as stored-not-parsed raw strings with no stated
// default of its own; UTC is the only default that keeps Next's
// result independent of the parsing process's local timezone.
func Parse(expr, tz string) (*Spec, error) {
	zone := tz
	if zone == "" {
		zone = "UTC"
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrInvalidTZ, tz, err)
	}
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrInvalidExpr, expr, err)
	}
	return &Spec{sched: sched, loc: loc}, nil
}

// Next reports the next fire time strictly after after, evaluated in
// the Spec's parsed location. It delegates to robfig's
// cron.Schedule.Next(after), which - for a valid 5-field standard
// expression - never returns the zero time.Time: every such expression
// fires again within (robfig's internal 5-year search bound
// notwithstanding) an effectively unbounded future, unlike
// scheduler.Every(nonPositiveDuration) or a spent scheduler.At list,
// which have a real "never again" case. So Spec.Next has no "never
// fires again" concept of its own to encode - see cronschedule_test.go's
// TestNextNeverReturnsZeroForValidExpression for the empirical
// confirmation this comment describes rather than assumes.
func (s *Spec) Next(after time.Time) time.Time {
	return s.sched.Next(after.In(s.loc))
}
