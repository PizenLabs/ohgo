package retrieval

import (
	"encoding/gob"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Field is one retrievable part of a document.
type Field int

// Retrieval fields. The order is part of the persisted schema.
const (
	// FieldPath is the repository path: directories, stem, and parts.
	FieldPath Field = iota
	// FieldIdentifier is the normalized identifier vocabulary of the source.
	FieldIdentifier
	// FieldComment is prose from line and block comments.
	FieldComment
	// FieldCount is the number of fields.
	FieldCount
)

// Weights are the BM25F field weights.
type Weights [FieldCount]float64

// DefaultWeights favour declarations and paths over prose. They are a starting
// point measured against the benchmark corpus, not a universal tuning.
var DefaultWeights = Weights{FieldPath: 1.5, FieldIdentifier: 1.0, FieldComment: 0.4}

// TermFrequency is one term's occurrence count per field.
type TermFrequency [FieldCount]uint32

// Sum reports the total occurrences used by hybrid signals.
func (tf TermFrequency) Sum() uint32 {
	var total uint32
	for _, value := range tf {
		total += value
	}
	return total
}

// Options bounds one retrieval index.
//
// The defaults are conventional Okapi BM25 parameters (k1=1.2, b=0.75). They
// are deliberately not treated as optimal for source code: the benchmark
// harness measures them, and a change here changes the persisted schema
// identity.
type Options struct {
	// K1 is the BM25 term-frequency saturation parameter.
	K1 float64
	// B is the BM25 document-length normalization parameter.
	B float64
	// Weights are the BM25F field weights.
	Weights Weights
	// MaxDocs bounds indexed documents.
	MaxDocs int
	// MaxTermsPerDoc bounds distinct terms retained per document.
	MaxTermsPerDoc int
	// MaxDocBytes bounds the source prefix read for one document.
	MaxDocBytes int64
	// MaxResults is the hard ceiling on one result page.
	MaxResults int
	// MaxQueryTerms bounds distinct query terms.
	MaxQueryTerms int
}

// DefaultOptions returns the bounded baseline configuration.
func DefaultOptions() Options {
	return Options{
		K1:             1.2,
		B:              0.75,
		Weights:        DefaultWeights,
		MaxDocs:        20000,
		MaxTermsPerDoc: 512,
		MaxDocBytes:    512 * 1024,
		MaxResults:     50,
		MaxQueryTerms:  32,
	}
}

// normalize fills unset values with the defaults and clamps hard ceilings.
func (o Options) normalize() Options {
	defaults := DefaultOptions()
	if o.K1 <= 0 {
		o.K1 = defaults.K1
	}
	if o.B < 0 || o.B > 1 {
		o.B = defaults.B
	}
	if o.Weights == (Weights{}) {
		o.Weights = defaults.Weights
	}
	if o.MaxDocs <= 0 {
		o.MaxDocs = defaults.MaxDocs
	}
	if o.MaxTermsPerDoc <= 0 {
		o.MaxTermsPerDoc = defaults.MaxTermsPerDoc
	}
	if o.MaxDocBytes <= 0 {
		o.MaxDocBytes = defaults.MaxDocBytes
	}
	if o.MaxResults <= 0 {
		o.MaxResults = defaults.MaxResults
	}
	if o.MaxResults > 200 {
		o.MaxResults = 200
	}
	if o.MaxQueryTerms <= 0 {
		o.MaxQueryTerms = defaults.MaxQueryTerms
	}
	if o.MaxQueryTerms > 64 {
		o.MaxQueryTerms = 64
	}
	return o
}

// Document is one indexed source document.
//
// Terms are the only retained representation. The source text itself is not
// stored: a consumer that needs to read a file reads it, and a cache cannot
// become a divergent copy of the repository.
type Document struct {
	// Path is the repository-relative identity.
	Path string
	// Language is the adapter language, for reporting only.
	Language string
	// Kind is the file classification (source, test, documentation, ...).
	Kind string
	// Test marks test source.
	Test bool
	// Generated marks a path convention that identifies generated output.
	Generated bool
	// Size and ModUnixNano are the freshness signature checked before reuse.
	Size        int64
	ModUnixNano int64
	// Length is the token count per field, including repeated tokens.
	Length [FieldCount]int
	// Terms maps a token to its per-field occurrence count.
	Terms map[string]TermFrequency
	// Truncated reports that Term retention hit MaxTermsPerDoc.
	Truncated bool
}

// clone returns a deep copy, so an index never aliases a caller's map.
func (d Document) clone() Document {
	copied := d
	copied.Terms = make(map[string]TermFrequency, len(d.Terms))
	for term, tf := range d.Terms {
		copied.Terms[term] = tf
	}
	return copied
}

// DocumentFromSource tokenizes one source file into a document.
//
// Identifiers and comments are separated, path segments form their own field,
// and only the bounded term set is retained. The signature is supplied by the
// indexer from a stat, not guessed from the content.
func DocumentFromSource(rel, language, kind string, test bool, size, modUnixNano int64, content []byte, opts Options) Document {
	opts = opts.normalize()
	doc := Document{
		Path:        rel,
		Language:    language,
		Kind:        kind,
		Test:        test,
		Size:        size,
		ModUnixNano: modUnixNano,
		Terms:       make(map[string]TermFrequency, 256),
	}
	add := func(tokens []string, field Field) {
		for _, token := range tokens {
			tf := doc.Terms[token]
			tf[field]++
			doc.Terms[token] = tf
			doc.Length[field]++
		}
	}
	add(PathTokens(rel), FieldPath)
	identifiers, comments := SourceTokens(content)
	add(identifiers, FieldIdentifier)
	add(comments, FieldComment)

	if len(doc.Terms) > opts.MaxTermsPerDoc {
		type scored struct {
			term  string
			total uint32
		}
		ranked := make([]scored, 0, len(doc.Terms))
		for term, tf := range doc.Terms {
			ranked = append(ranked, scored{term: term, total: tf.Sum()})
		}
		sort.Slice(ranked, func(i, j int) bool {
			if ranked[i].total != ranked[j].total {
				return ranked[i].total > ranked[j].total
			}
			return ranked[i].term < ranked[j].term
		})
		keep := make(map[string]TermFrequency, opts.MaxTermsPerDoc)
		for _, entry := range ranked[:opts.MaxTermsPerDoc] {
			keep[entry.term] = doc.Terms[entry.term]
		}
		doc.Terms = keep
		doc.Truncated = true
	}
	return doc
}

// posting is one document's contribution to one term.
type posting struct {
	doc int32
	tf  TermFrequency
}

// Index is a bounded, deterministic BM25F index.
//
// It is safe for concurrent readers and serialized writers. Updates are
// incremental: only the affected document's postings are removed and rebuilt,
// so one changed file never triggers a corpus rebuild.
type Index struct {
	mu   sync.RWMutex
	opts Options
	// ids maps a path to its stable record slot. records holds the documents;
	// postings reference the integer slot, not the path. A path string costs a
	// header per posting and a map lookup per candidate; an int32 costs 4 bytes
	// and one slice index.
	ids      map[string]int32
	records  []Document
	free     []int32
	postings map[string][]posting
	df       map[string]int
	total    [FieldCount]int64
	trunc    int
}

// NewIndex returns an empty index over opts.
func NewIndex(opts Options) *Index {
	return &Index{
		opts:     opts.normalize(),
		ids:      make(map[string]int32),
		postings: make(map[string][]posting),
		df:       make(map[string]int),
	}
}

// Options reports the normalized configuration.
func (idx *Index) Options() Options {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.opts
}

// Apply updates and removes documents atomically under one write lock.
//
// The caller may stage reads and tokenization outside the lock; readers see
// either the previous corpus or the complete new one, never a half-applied
// batch.
func (idx *Index) Apply(updates []Document, removals []string) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	touched := make(map[string]struct{}, 32)
	for _, rel := range removals {
		idx.removeLocked(rel, touched)
	}
	for _, update := range updates {
		if update.Path == "" {
			return errors.New("retrieval: document has no path")
		}
		if _, exists := idx.ids[update.Path]; !exists && len(idx.ids) >= idx.opts.MaxDocs {
			return ErrCapacity
		}
		idx.removeLocked(update.Path, touched)
		doc := update.clone()
		id := idx.allocateLocked()
		idx.ids[doc.Path] = id
		idx.records[id] = doc
		if doc.Truncated {
			idx.trunc++
		}
		for field := Field(0); field < FieldCount; field++ {
			idx.total[field] += int64(doc.Length[field])
		}
		for term, tf := range doc.Terms {
			idx.postings[term] = append(idx.postings[term], posting{doc: id, tf: tf})
			idx.df[term]++
			touched[term] = struct{}{}
		}
	}
	// Sort only the lists this batch changed. Sorting the whole corpus would
	// turn one changed file into global work, which retrieval must avoid.
	for term := range touched {
		list := idx.postings[term]
		sort.Slice(list, func(i, j int) bool { return list[i].doc < list[j].doc })
		idx.postings[term] = list
	}
	return nil
}

// bulkAppend inserts one document without sorting its posting lists.
//
// Load uses it so rebuilding a large corpus sorts each posting list once
// instead of once per document; that per-document sort is quadratic in the
// number of documents sharing a term.
func (idx *Index) bulkAppend(doc Document) {
	id := idx.allocateLocked()
	idx.ids[doc.Path] = id
	idx.records[id] = doc
	if doc.Truncated {
		idx.trunc++
	}
	for field := Field(0); field < FieldCount; field++ {
		idx.total[field] += int64(doc.Length[field])
	}
	for term, tf := range doc.Terms {
		idx.postings[term] = append(idx.postings[term], posting{doc: id, tf: tf})
		idx.df[term]++
	}
}

// bulkSort sorts every posting list once after a bulk load.
func (idx *Index) bulkSort() {
	for term, list := range idx.postings {
		sort.Slice(list, func(i, j int) bool { return list[i].doc < list[j].doc })
		idx.postings[term] = list
	}
}

// Update inserts or replaces one document.
func (idx *Index) Update(doc Document) error {
	return idx.Apply([]Document{doc}, nil)
}

// Remove deletes one document. Removing an absent path is a no-op.
func (idx *Index) Remove(rel string) error {
	return idx.Apply(nil, []string{rel})
}

// removeLocked removes one document's postings and totals, recording the terms
// whose posting lists changed.
func (idx *Index) removeLocked(rel string, touched map[string]struct{}) {
	id, ok := idx.ids[rel]
	if !ok {
		return
	}
	doc := idx.records[id]
	if doc.Truncated {
		idx.trunc--
	}
	for field := Field(0); field < FieldCount; field++ {
		idx.total[field] -= int64(doc.Length[field])
	}
	for term := range doc.Terms {
		touched[term] = struct{}{}
		list := idx.postings[term]
		kept := list[:0]
		for _, entry := range list {
			if entry.doc != id {
				kept = append(kept, entry)
			}
		}
		if len(kept) == 0 {
			delete(idx.postings, term)
			delete(idx.df, term)
		} else {
			idx.postings[term] = kept
			idx.df[term] = len(kept)
		}
	}
	delete(idx.ids, rel)
	idx.records[id] = Document{}
	idx.free = append(idx.free, id)
}

// allocateLocked returns a record slot, reusing a freed slot when possible.
func (idx *Index) allocateLocked() int32 {
	if len(idx.free) > 0 {
		id := idx.free[len(idx.free)-1]
		idx.free = idx.free[:len(idx.free)-1]
		return id
	}
	idx.records = append(idx.records, Document{})
	return int32(len(idx.records) - 1)
}

// Stats is a point-in-time index summary.
type Stats struct {
	Documents     int
	Terms         int
	Postings      int
	TruncatedDocs int
	TotalLength   [FieldCount]int64
}

// Stats reports index size without exposing mutable state.
func (idx *Index) Stats() Stats {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	stats := Stats{
		Documents:     len(idx.ids),
		Terms:         len(idx.df),
		TruncatedDocs: idx.trunc,
		TotalLength:   idx.total,
	}
	for _, list := range idx.postings {
		stats.Postings += len(list)
	}
	return stats
}

// Errors returned by retrieval operations.
var (
	// ErrCapacity reports that MaxDocs would be exceeded.
	ErrCapacity = errors.New("retrieval: document capacity reached")
	// ErrEmptyQuery reports a query with no usable term.
	ErrEmptyQuery = errors.New("retrieval: query has no searchable term")
	// ErrCacheIdentity reports a persisted index that belongs to another
	// schema, root, or configuration.
	ErrCacheIdentity = errors.New("retrieval: cache identity mismatch")
)

const (
	cacheSchema   = "retrieval/v2"
	cacheFileName = "retrieval-index.gob"
)

// envelope is the persisted identity plus documents.
type envelope struct {
	Schema  string
	Root    string
	Options Options
	Docs    []storedDocument
}

// storedDocument is the columnar cache form of Document.
//
// Gob decodes a map[string]TermFrequency by allocating a map entry at a time,
// which dominates warm load on a large corpus. Flattened sorted slices decode
// several times faster and remain deterministic; the in-memory Document shape
// is rebuilt only for the documents that are actually loaded.
type storedDocument struct {
	Path        string
	Language    string
	Kind        string
	Test        bool
	Generated   bool
	Size        int64
	ModUnixNano int64
	Length      [FieldCount]int
	Terms       []string
	Frequency   []TermFrequency
	Truncated   bool
}

// Save writes the index atomically into dir.
//
// Persistence is an optimization, never evidence: a load failure degrades to a
// rebuild. The envelope carries the schema, root, and options so a cache from
// another workspace, adapter set, or tuning is rejected rather than silently
// mixed.
func (idx *Index) Save(dir, root string) error {
	if dir == "" {
		return nil
	}
	idx.mu.RLock()
	docs := make([]storedDocument, 0, len(idx.ids))
	for _, doc := range idx.records {
		if doc.Path == "" {
			continue
		}
		stored := storedDocument{
			Path: doc.Path, Language: doc.Language, Kind: doc.Kind,
			Test: doc.Test, Generated: doc.Generated, Size: doc.Size,
			ModUnixNano: doc.ModUnixNano, Length: doc.Length, Truncated: doc.Truncated,
			Terms: make([]string, 0, len(doc.Terms)), Frequency: make([]TermFrequency, 0, len(doc.Terms)),
		}
		for term, tf := range doc.Terms {
			stored.Terms = append(stored.Terms, term)
			stored.Frequency = append(stored.Frequency, tf)
		}
		order := make([]int, len(stored.Terms))
		for i := range order {
			order[i] = i
		}
		sort.Slice(order, func(i, j int) bool { return stored.Terms[order[i]] < stored.Terms[order[j]] })
		terms := make([]string, len(order))
		frequency := make([]TermFrequency, len(order))
		for i, source := range order {
			terms[i], frequency[i] = stored.Terms[source], stored.Frequency[source]
		}
		stored.Terms, stored.Frequency = terms, frequency
		docs = append(docs, stored)
	}
	opts := idx.opts
	idx.mu.RUnlock()
	sort.Slice(docs, func(i, j int) bool { return docs[i].Path < docs[j].Path })

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("retrieval: create cache dir %q: %w", dir, err)
	}
	temp, err := os.CreateTemp(dir, ".retrieval-*")
	if err != nil {
		return fmt.Errorf("retrieval: create cache entry: %w", err)
	}
	name := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(name)
		}
	}()
	encoder := gob.NewEncoder(temp)
	if err := encoder.Encode(envelope{Schema: cacheSchema, Root: root, Options: opts, Docs: docs}); err != nil {
		_ = temp.Close()
		return fmt.Errorf("retrieval: encode cache: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("retrieval: close cache entry: %w", err)
	}
	if err := os.Rename(name, filepath.Join(dir, cacheFileName)); err != nil {
		return fmt.Errorf("retrieval: publish cache entry: %w", err)
	}
	cleanup = false
	return nil
}

// Load reads a persisted index for root from dir and rejects a cache built
// with a different configuration.
func Load(dir, root string, opts Options) (*Index, error) {
	if dir == "" {
		return nil, os.ErrNotExist
	}
	file, err := os.Open(filepath.Join(dir, cacheFileName))
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	var stored envelope
	if err := gob.NewDecoder(file).Decode(&stored); err != nil {
		return nil, fmt.Errorf("retrieval: decode cache: %w", err)
	}
	if stored.Schema != cacheSchema || stored.Root != root {
		return nil, fmt.Errorf("%w: schema %q root %q", ErrCacheIdentity, stored.Schema, stored.Root)
	}
	opts = opts.normalize()
	if stored.Options != opts {
		return nil, fmt.Errorf("%w: configuration changed", ErrCacheIdentity)
	}
	if len(stored.Docs) > opts.MaxDocs {
		return nil, fmt.Errorf("%w: %d documents exceed %d", ErrCacheIdentity, len(stored.Docs), opts.MaxDocs)
	}
	idx := NewIndex(opts)
	for _, stored := range stored.Docs {
		if stored.Path == "" || len(stored.Terms) > opts.MaxTermsPerDoc || len(stored.Terms) != len(stored.Frequency) {
			return nil, fmt.Errorf("%w: malformed document %q", ErrCacheIdentity, stored.Path)
		}
		doc := Document{
			Path: stored.Path, Language: stored.Language, Kind: stored.Kind,
			Test: stored.Test, Generated: stored.Generated, Size: stored.Size,
			ModUnixNano: stored.ModUnixNano, Length: stored.Length, Truncated: stored.Truncated,
			Terms: make(map[string]TermFrequency, len(stored.Terms)),
		}
		for i, term := range stored.Terms {
			doc.Terms[term] = stored.Frequency[i]
		}
		idx.bulkAppend(doc)
	}
	idx.bulkSort()
	return idx, nil
}

// Signature reports the freshness signature of one indexed document.
//
// It is a cheap read for reconciliation: copying the whole term map just to
// compare a stat would make an unchanged sync proportional to the corpus.
func (idx *Index) Signature(rel string) (size, modUnixNano int64, ok bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	id, found := idx.ids[rel]
	if !found {
		return 0, 0, false
	}
	doc := idx.records[id]
	return doc.Size, doc.ModUnixNano, true
}

// Document returns a copy of one indexed document.
func (idx *Index) Document(rel string) (Document, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	id, ok := idx.ids[rel]
	if !ok {
		return Document{}, false
	}
	return idx.records[id].clone(), true
}

// Paths returns the indexed paths in sorted order.
func (idx *Index) Paths() []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	paths := make([]string, 0, len(idx.ids))
	for rel := range idx.ids {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	return paths
}
