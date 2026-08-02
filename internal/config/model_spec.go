package config

// The configured model object. It is a CLOSED shape: every key is spelled out
// below and anything else is a hard error, so a typo cannot silently disable a
// setting the operator believes is applied.

import (
	"fmt"
	"strconv"

	"github.com/MiviaLabs/mivia-agent/internal/reasoning"
	"github.com/pelletier/go-toml/v2/unstable"
)

// ModelSpec is one explicitly configured provider model and its physical
// context capacity. The name is provider-qualified by its containing group.
type ModelSpec struct {
	Name                string `toml:"name"`
	ContextWindowTokens int    `toml:"context_window_tokens"`
	MaxOutputTokens     int    `toml:"max_output_tokens,omitempty"`
	// ReasoningEfforts is the ordered set of reasoning levels this model
	// offers. Empty means the model has no reasoning surface. Order is
	// preserved because it is the order the /effort picker lists.
	ReasoningEfforts []reasoning.Level `toml:"reasoning_efforts,omitempty"`
	// Reasoning is this model's DEFAULT effort and must be a member of
	// ReasoningEfforts. Empty means the model ships with no reasoning field
	// sent, which a model that offers efforts may still choose - the user
	// opts in through /effort. Reasoning belongs to the model rather than to
	// [chat] because capabilities and value sets differ per model, so one
	// session-global value would be wrong for every model it did not match.
	Reasoning reasoning.Level `toml:"reasoning,omitempty"`
	// ReasoningDialect is this model's wire shape. Empty uses the provider's
	// vetted default where one exists; load refuses an active level with no
	// resolvable dialect rather than letting the key silently do nothing.
	ReasoningDialect reasoning.Dialect `toml:"reasoning_dialect,omitempty"`
}

// UnmarshalTOML enforces the narrow model object shape. A scalar model array
// is rejected instead of being silently treated as an empty catalog.
func (m *ModelSpec) UnmarshalTOML(value *unstable.Node) error {
	if value == nil || (value.Kind != unstable.InlineTable && value.Kind != unstable.Table) {
		return fmt.Errorf("model must be an object")
	}
	var name string
	var context int
	maxOutput := 0
	var level reasoning.Level
	var dialect reasoning.Dialect
	var efforts []reasoning.Level
	for child := value.Child(); child != nil; child = child.Next() {
		key := child.Key()
		keyNode := key.Node()
		if keyNode == nil {
			return fmt.Errorf("invalid model object")
		}
		valueNode := child.Value()
		switch string(keyNode.Data) {
		case "name":
			if valueNode.Kind != unstable.String {
				return fmt.Errorf("invalid model object")
			}
			name = string(valueNode.Data)
		case "context_window_tokens":
			if valueNode.Kind != unstable.Integer {
				return fmt.Errorf("invalid model object")
			}
			parsed, err := strconv.Atoi(string(valueNode.Data))
			if err != nil {
				return fmt.Errorf("invalid model object")
			}
			context = parsed
		case "max_output_tokens":
			if valueNode.Kind != unstable.Integer {
				return fmt.Errorf("invalid model object")
			}
			parsed, err := strconv.Atoi(string(valueNode.Data))
			if err != nil {
				return fmt.Errorf("invalid model object")
			}
			maxOutput = parsed
		case "reasoning":
			if valueNode.Kind != unstable.String {
				return fmt.Errorf("invalid model object")
			}
			parsed, err := reasoning.ParseLevel(string(valueNode.Data))
			if err != nil {
				return err
			}
			level = parsed
		case "reasoning_dialect":
			if valueNode.Kind != unstable.String {
				return fmt.Errorf("invalid model object")
			}
			parsed, err := reasoning.ParseDialect(string(valueNode.Data))
			if err != nil {
				return err
			}
			dialect = parsed
		case "reasoning_efforts":
			parsed, err := decodeReasoningEfforts(valueNode)
			if err != nil {
				return err
			}
			// An explicitly empty array is the same statement as omitting the
			// key, and normalizing it to nil keeps "offers nothing" a single
			// representation for every downstream check.
			efforts = parsed
		default:
			return fmt.Errorf("invalid model object")
		}
	}
	m.Name = name
	m.ContextWindowTokens = context
	m.MaxOutputTokens = maxOutput
	m.Reasoning = level
	m.ReasoningDialect = dialect
	m.ReasoningEfforts = efforts
	return nil
}

// decodeReasoningEfforts reads the declared effort array. Every entry must be
// a known level and the set must not repeat one: a duplicate would render two
// identical rows in the /effort picker.
func decodeReasoningEfforts(value *unstable.Node) ([]reasoning.Level, error) {
	if value == nil || value.Kind != unstable.Array {
		return nil, fmt.Errorf("reasoning_efforts must be an array of levels")
	}
	var efforts []reasoning.Level
	seen := map[reasoning.Level]struct{}{}
	for item := value.Children(); item.Next(); {
		node := item.Node()
		if node == nil || node.Kind != unstable.String {
			return nil, fmt.Errorf("reasoning_efforts must be an array of levels")
		}
		level, err := reasoning.ParseLevel(string(node.Data))
		if err != nil {
			return nil, err
		}
		if !level.Active() {
			return nil, fmt.Errorf("reasoning_efforts must not contain an empty level")
		}
		if _, duplicate := seen[level]; duplicate {
			return nil, fmt.Errorf("reasoning_efforts repeats %q", level)
		}
		seen[level] = struct{}{}
		efforts = append(efforts, level)
	}
	return efforts, nil
}
