package memory

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// Result is one search hit.
type Result struct {
	ID      string
	Scope   Scope
	Org     string
	Title   string
	Verdict Verdict
	Tags    []string
	Created string
	Snippet string
}

// Query is a search request.
type Query struct {
	Text       string
	Scope      Scope
	MaxResults int
}

// Store is a memory backend. Implementations must be safe for concurrent use.
type Store interface {
	Save(context.Context, Entry) (Result, error)
	Search(context.Context, Query) ([]Result, error)
	Count(context.Context, Scope) (int, error)
	PromoteToCore(context.Context, string) error
	CoreEntries(context.Context, Scope) ([]Result, error)
	Delete(context.Context, string) error
	Close() error
}

// Config selects the backend and bounds.
type Config struct {
	// Backend is "memory" for the process-local backend. Durable Markdown
	// storage is opened by the cliagents adapter.
	Backend          string
	OrgID            string
	MaxEntryBytes    int
	MaxEntries       int
	MaxSearchResults int
	BlockPatterns    []string
	ReadOnly         bool
}

const (
	BackendMemory           = "memory"
	BackendMarkdown         = "markdown"
	DefaultMaxEntries       = 500
	DefaultMaxSearchResults = 8
)

// Open returns a process-local Store. Durable storage uses the Markdown
// adapter in internal/cliagents.
func Open(cfg Config) (Store, error) {
	cfg = normalizeConfig(cfg)
	if cfg.OrgID != "" {
		norm, err := NormalizeOrgID(cfg.OrgID)
		if err != nil {
			return nil, fmt.Errorf("memory org_id: %w", err)
		}
		cfg.OrgID = norm
	}
	for _, pattern := range cfg.BlockPatterns {
		if _, err := regexp.Compile(pattern); err != nil {
			return nil, fmt.Errorf("memory block pattern %q: %w", pattern, err)
		}
	}
	if cfg.Backend != BackendMemory {
		return nil, fmt.Errorf("memory backend %q: must be \"memory\"", cfg.Backend)
	}
	return newMemStore(cfg), nil
}

func normalizeConfig(cfg Config) Config {
	cfg.Backend = strings.ToLower(strings.TrimSpace(cfg.Backend))
	if cfg.Backend == "" {
		cfg.Backend = BackendMemory
	}
	if cfg.MaxEntryBytes <= 0 {
		cfg.MaxEntryBytes = DefaultMaxEntryBytes
	}
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = DefaultMaxEntries
	}
	if cfg.MaxSearchResults <= 0 {
		cfg.MaxSearchResults = DefaultMaxSearchResults
	}
	return cfg
}

func (cfg Config) limits() Limits {
	return Limits{MaxEntryBytes: cfg.MaxEntryBytes, BlockPatterns: cfg.BlockPatterns}
}
