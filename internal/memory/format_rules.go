package memory

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Format constants for memory entries.
// These constants define the single source of truth for the memory file format,
// matching the rules enforced across the Go package and scripts/check_memories.py.
const (
	// DefaultMaxEntryBytes is the default byte limit for a rendered memory entry.
	DefaultMaxEntryBytes = 8192

	// MaxTitleLen is the maximum allowed rune length for an entry title.
	MaxTitleLen = 120
	// MaxSummaryLen is the maximum allowed rune length for an entry summary.
	MaxSummaryLen = 400
	// MaxWhyLen is the maximum allowed rune length for the entry why field.
	MaxWhyLen = 1000
	// MaxBodyFieldLen is the maximum allowed rune length for good/bad body fields.
	MaxBodyFieldLen = 2000

	// MaxTags is the maximum number of tags an entry may declare.
	MaxTags = 8
	// MaxTagLen is the maximum allowed rune length for an individual tag.
	MaxTagLen = 32

	// MaxReferences is the maximum number of references an entry may declare.
	MaxReferences = 8
	// MaxReferenceLen is the maximum allowed rune length for an individual reference.
	MaxReferenceLen = 200

	// MaxRelated is the maximum number of related memories an entry may list.
	MaxRelated = 16
	// MaxRelatedIDLen is the maximum allowed rune length for a related memory id.
	MaxRelatedIDLen = 120

	// MinEntryBytes is the lower bound for custom MaxEntryBytes limits.
	MinEntryBytes = 256
	// MaxEntryBytesCap is the upper bound for custom MaxEntryBytes limits.
	MaxEntryBytesCap = 65536
)

// ValidateRelatedIDFormat validates that an individual related memory ID conforms
// to the plain ID rules (length, no line controls, no forbidden characters).
func ValidateRelatedIDFormat(rel string) error {
	if rel == "" || utf8.RuneCountInString(rel) > MaxRelatedIDLen {
		return fmt.Errorf("each related id must be 1-%d characters", MaxRelatedIDLen)
	}
	if hasLineControl(rel) {
		return fmt.Errorf("related id must not contain line breaks")
	}
	if strings.ContainsAny(rel, ",:[]{}") || strings.Contains(rel, " #") {
		return fmt.Errorf("related id must be a plain id without any of , : [ ] { } or \" #\"")
	}
	return nil
}

// ValidateStoreRelations checks a collection of scanned documents for cross-file
// integrity rules:
// 1. Dangling target: every ID listed in Related must exist in the store.
// 2. Reciprocity: if entry A lists entry B in Related, entry B must list entry A.
func ValidateStoreRelations(docs []MarkdownDocument) error {
	seen := make(map[string]struct{}, len(docs))
	relatedByID := make(map[string]map[string]struct{}, len(docs))

	for _, doc := range docs {
		id := doc.ID
		if id == "" {
			stem := strings.TrimSuffix(filepath.Base(doc.Path), ".md")
			id = strings.ReplaceAll(stem, "-", "_")
		}
		seen[id] = struct{}{}
		for _, target := range doc.Entry.Related {
			target = strings.TrimSpace(target)
			if target == "" {
				continue
			}
			if relatedByID[id] == nil {
				relatedByID[id] = make(map[string]struct{})
			}
			relatedByID[id][target] = struct{}{}
		}
	}

	for id, targets := range relatedByID {
		for target := range targets {
			if _, exists := seen[target]; !exists {
				return fmt.Errorf("memory %q: related names %q, which is not the id of any memory in this store (dangling target)", id, target)
			}
			if targetLinks, ok := relatedByID[target]; !ok || func() bool {
				_, linkedBack := targetLinks[id]
				return !linkedBack
			}() {
				return fmt.Errorf("memory %q: related link to %q is asymmetric: %q does not link back to %q (reciprocal linking is mandatory)", id, target, target, id)
			}
		}
	}
	return nil
}

// ValidateEntryRelations validates an incoming entry (with prospective ID incomingID)
// against the existing store documents for dangling targets and reciprocity.
// It refuses saving an entry that would introduce a dangling link or an asymmetric relationship.
func ValidateEntryRelations(existingDocs []MarkdownDocument, incoming Entry, incomingID string) error {
	// Build simulated set of documents after saving/updating this entry.
	var simDocs []MarkdownDocument
	found := false
	for _, doc := range existingDocs {
		docID := doc.ID
		if docID == "" {
			stem := strings.TrimSuffix(filepath.Base(doc.Path), ".md")
			docID = strings.ReplaceAll(stem, "-", "_")
		}
		if docID == incomingID {
			simDocs = append(simDocs, MarkdownDocument{
				Path:  doc.Path,
				ID:    incomingID,
				Entry: incoming,
			})
			found = true
		} else {
			simDocs = append(simDocs, doc)
		}
	}
	if !found {
		simDocs = append(simDocs, MarkdownDocument{
			ID:    incomingID,
			Entry: incoming,
		})
	}
	return ValidateStoreRelations(simDocs)
}
