package skills

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

const (
	resourceManifestName       = "resources.toml"
	maxResourceManifestBytes   = 32 << 10
	maxResourceCount           = 32
	maxResourceSummaryBytes    = 200
	maxResourceBytes           = 64 << 10
	maxActivationResourceBytes = 128 << 10
	resourceToolResultBytes    = maxResourceBytes + 128
)

var nextActivationID atomic.Uint64

// ResourceDescriptor is the safe, model-visible description of one declared
// skill resource. The backing path is deliberately private.
type ResourceDescriptor struct {
	ID      string
	Summary string
	path    string
}

// skillLocation binds a loaded definition to the exact directory that supplied
// its instructions. It is never rendered or serialized.
type skillLocation struct {
	path string
	info fs.FileInfo
}

// ResourceContent is a bounded, activation-local resource value. It contains
// no filesystem location and is never retained after its activation closes.
type ResourceContent struct {
	ID     string
	Text   string
	Size   int
	Digest string
}

// ResourceSnapshot is the durable, path-free value of one declared resource.
// It contains only the model-safe identifier, body, and content digest.
type ResourceSnapshot struct {
	ID     string
	Text   string
	Digest string
}

// SnapshotResources returns durable values for all declared resources. It
// reads them through an activation so the resource path stays private.
func (d Definition) SnapshotResources(ctx context.Context) ([]ResourceSnapshot, error) {
	if len(d.Resources) == 0 {
		return nil, nil
	}
	activation, err := d.Activate()
	if err != nil {
		return nil, err
	}
	defer activation.Close()
	snapshots := make([]ResourceSnapshot, 0, len(d.Resources))
	for _, resource := range d.Resources {
		content, err := activation.Read(ctx, resource.ID)
		if err != nil {
			return nil, err
		}
		snapshots = append(snapshots, ResourceSnapshot{ID: content.ID, Text: content.Text, Digest: content.Digest})
	}
	return snapshots, nil
}

// SkillActivation is an opaque, per-invocation capability for one selected
// skill. It owns the pinned resource root, cache, and aggregate byte budget.
type SkillActivation struct {
	definition Definition
	root       *os.File
	rootFS     *os.Root
	resources  map[string]ResourceDescriptor
	cache      map[string]ResourceContent
	used       int
	closed     bool
	key        string
	mu         sync.Mutex
}

// Activate binds this loaded definition to the same directory identity that
// supplied SKILL.md. Replaced or symlinked directories fail closed.
func (d Definition) Activate() (*SkillActivation, error) {
	if d.location.path == "" || d.location.info == nil {
		return nil, fmt.Errorf("skill resources are unavailable")
	}
	root, rootFS, err := openPinnedResourceRoot(d.location)
	if err != nil {
		return nil, fmt.Errorf("skill resources are unavailable")
	}
	resources := make(map[string]ResourceDescriptor, len(d.Resources))
	for _, resource := range d.Resources {
		resources[resource.ID] = resource
	}
	return &SkillActivation{
		definition: d,
		root:       root,
		rootFS:     rootFS,
		resources:  resources,
		cache:      make(map[string]ResourceContent, len(resources)),
		key:        fmt.Sprintf("skill-resource:%d", nextActivationID.Add(1)),
	}, nil
}

// Prompt renders base instructions, optionally with the bounded catalogue that
// is valid only while this activation's scoped reader is present.
func (a *SkillActivation) Prompt(withResources bool) string {
	if a == nil || !withResources || len(a.resources) == 0 {
		if a == nil {
			return ""
		}
		return a.definition.Instructions
	}
	var b strings.Builder
	b.WriteString(a.definition.Instructions)
	b.WriteString("\n\n<skill-resources>\n")
	for _, resource := range a.definition.Resources {
		b.WriteString("- id: ")
		b.WriteString(resource.ID)
		b.WriteString("\n  purpose: ")
		b.WriteString(resource.Summary)
		b.WriteByte('\n')
	}
	b.WriteString("</skill-resources>")
	return b.String()
}

// Read returns only a declared, bounded UTF-8 text resource. A single mutex
// intentionally serializes v1's tiny reads so duplicate cache fills and quota
// accounting are atomic without a concurrent future framework.
func (a *SkillActivation) Read(ctx context.Context, id string) (ResourceContent, error) {
	if err := ctx.Err(); err != nil {
		return ResourceContent{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ResourceContent{}, fmt.Errorf("skill resource activation is closed")
	}
	resource, ok := a.resources[id]
	if !ok {
		return ResourceContent{}, fmt.Errorf("unknown skill resource")
	}
	if cached, ok := a.cache[id]; ok {
		return cached, nil
	}
	data, err := readDeclaredResource(a.root, a.rootFS, resource.path, maxResourceBytes)
	if err != nil {
		return ResourceContent{}, fmt.Errorf("skill resource %q cannot be read", id)
	}
	if err := ctx.Err(); err != nil {
		return ResourceContent{}, err
	}
	if a.used+len(data) > maxActivationResourceBytes {
		return ResourceContent{}, fmt.Errorf("skill resource quota exceeded")
	}
	text := string(data)
	if !validResourceText(text) {
		return ResourceContent{}, fmt.Errorf("skill resource %q is not valid text", id)
	}
	digest := sha256.Sum256(data)
	content := ResourceContent{ID: id, Text: text, Size: len(data), Digest: hex.EncodeToString(digest[:])}
	a.cache[id] = content
	a.used += len(data)
	return content, nil
}

// ToolKey is opaque scheduling metadata for this activation's one scoped
// tool. It is never included in a model-facing prompt or event.
func (a *SkillActivation) ToolKey() string {
	if a == nil {
		return ""
	}
	return a.key
}

// ToolResultBudget is the bounded resource body plus fixed tool framing.
func (a *SkillActivation) ToolResultBudget() int { return resourceToolResultBytes }

// Close revokes the activation and releases its descriptor-relative root.
func (a *SkillActivation) Close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	a.cache = nil
	a.resources = nil
	root := a.root
	a.root = nil
	rootFS := a.rootFS
	a.rootFS = nil
	a.mu.Unlock()
	if root != nil {
		_ = root.Close()
	}
	if rootFS != nil {
		_ = rootFS.Close()
	}
}

func readDeclaredResource(root *os.File, rootFS *os.Root, resourcePath string, maxBytes int) ([]byte, error) {
	file, err := openDeclaredResourceFile(root, rootFS, resourcePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("resource exceeds byte limit")
	}
	return data, nil
}

func openPinnedResourceRoot(location skillLocation) (*os.File, *os.Root, error) {
	info, err := os.Lstat(location.path)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() || !os.SameFile(location.info, info) {
		return nil, nil, fmt.Errorf("resource root is unavailable")
	}
	root, err := os.Open(location.path)
	if err != nil {
		return nil, nil, err
	}
	rootFS, err := os.OpenRoot(location.path)
	if err != nil {
		_ = root.Close()
		return nil, nil, err
	}
	opened, fileErr := root.Stat()
	rooted, rootErr := rootFS.Lstat(".")
	if fileErr != nil || rootErr != nil || !opened.IsDir() || !rooted.IsDir() || !os.SameFile(location.info, opened) || !os.SameFile(location.info, rooted) {
		_ = root.Close()
		_ = rootFS.Close()
		return nil, nil, fmt.Errorf("resource root identity changed")
	}
	return root, rootFS, nil
}

func loadDeclaredResources(location skillLocation) ([]ResourceDescriptor, string) {
	dir, dirFS, err := openPinnedResourceRoot(location)
	if os.IsNotExist(err) {
		return nil, ""
	}
	if err != nil {
		return nil, "ignore invalid skill resources"
	}
	defer dir.Close()
	defer dirFS.Close()
	data, err := readDeclaredResource(dir, dirFS, resourceManifestName, maxResourceManifestBytes)
	if os.IsNotExist(err) {
		return nil, ""
	}
	if err != nil {
		return nil, "ignore invalid skill resources"
	}
	resources, err := parseResourceManifest(data)
	if err != nil {
		return nil, "ignore invalid skill resources"
	}
	return resources, ""
}

func validResourceText(text string) bool {
	if !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 {
		return false
	}
	for _, r := range text {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

type resourceManifest struct {
	Format    int             `toml:"format"`
	Resources []resourceEntry `toml:"resources"`
}

type resourceEntry struct {
	ID      string `toml:"id"`
	Path    string `toml:"path"`
	Summary string `toml:"summary"`
}

// parseResourceManifest accepts the deliberately small v1 declaration
// language. The project's TOML decoder rejects unknown fields, duplicate keys,
// and malformed types before descriptors reach model-facing code.
func parseResourceManifest(data []byte) ([]ResourceDescriptor, error) {
	var manifest resourceManifest
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(&manifest); err != nil {
		return nil, fmt.Errorf("invalid resources manifest")
	}
	if manifest.Format != 1 {
		return nil, fmt.Errorf("resources manifest format must be 1")
	}
	if len(manifest.Resources) > maxResourceCount {
		return nil, fmt.Errorf("resources manifest resources are invalid")
	}
	descriptors := make([]ResourceDescriptor, 0, len(manifest.Resources))
	seenIDs := make(map[string]struct{}, len(manifest.Resources))
	seenPaths := make(map[string]struct{}, len(manifest.Resources))
	for _, entry := range manifest.Resources {
		if !validResourceID(entry.ID) || !validResourcePath(entry.Path) {
			return nil, fmt.Errorf("resource entry is invalid")
		}
		cleanSummary, truncated := SanitizeModelFacingText(entry.Summary, maxResourceSummaryBytes)
		if truncated || cleanSummary == "" || cleanSummary != strings.TrimSpace(entry.Summary) {
			return nil, fmt.Errorf("resource summary is invalid")
		}
		if _, duplicate := seenIDs[entry.ID]; duplicate {
			return nil, fmt.Errorf("resource IDs must be unique")
		}
		if _, duplicate := seenPaths[entry.Path]; duplicate {
			return nil, fmt.Errorf("resource paths must be unique")
		}
		seenIDs[entry.ID] = struct{}{}
		seenPaths[entry.Path] = struct{}{}
		descriptors = append(descriptors, ResourceDescriptor{ID: entry.ID, Summary: cleanSummary, path: entry.Path})
	}
	return descriptors, nil
}

func validResourceID(id string) bool {
	if id == "" || len(id) > 64 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func validResourcePath(value string) bool {
	if value == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return false
	}
	clean := pathpkg.Clean(value)
	if clean != value || clean == "." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return false
	}
	for _, component := range strings.Split(clean, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}
