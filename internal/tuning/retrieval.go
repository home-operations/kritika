package tuning

import "fmt"

// ContextPack holds the tunable values of package contextpack: the
// similar-code bounds that the gateway enforces on one request and a run
// keeps to, and the defaults of a context pack's Options.
type ContextPack struct {
	// SimilarQueries is how many texts one similar-code request embeds.
	SimilarQueries int `env:"KRITIKA_TUNING_CONTEXT_PACK_SIMILAR_QUERIES" envDefault:"12" yaml:"similarQueries,omitempty"`
	// SimilarQueryChars is how many bytes of each text are embedded.
	SimilarQueryChars int `env:"KRITIKA_TUNING_CONTEXT_PACK_SIMILAR_QUERY_CHARS" envDefault:"3000" yaml:"similarQueryChars,omitempty"`
	// SimilarLimit is the most chunks a request can ask the answer to hold.
	// A run asks for more than its prompt shows, as it drops those that the
	// head no longer has or that its other stages show.
	SimilarLimit int `env:"KRITIKA_TUNING_CONTEXT_PACK_SIMILAR_LIMIT" envDefault:"16" yaml:"similarLimit,omitempty"`

	// The pack's defaults, which with MaxPackBytes's fit a model's
	// context with room for the diff.

	// MaxDeclLines is the largest declaration used whole. A bigger one
	// gets a window around the change instead.
	MaxDeclLines int `env:"KRITIKA_TUNING_CONTEXT_PACK_MAX_DECL_LINES" envDefault:"150" yaml:"maxDeclLines,omitempty"`
	// WindowRadius is the fixed-window fallback's reach on each side.
	WindowRadius int `env:"KRITIKA_TUNING_CONTEXT_PACK_WINDOW_RADIUS" envDefault:"20" yaml:"windowRadius,omitempty"`
	// MaxIdentifiers caps the definition lookups of a review, most
	// frequent first.
	MaxIdentifiers int `env:"KRITIKA_TUNING_CONTEXT_PACK_MAX_IDENTIFIERS" envDefault:"40" yaml:"maxIdentifiers,omitempty"`
	// DefinitionsPerName, the definitions of one identifier, and
	// CallersPerSymbol cap each lookup.
	DefinitionsPerName int `env:"KRITIKA_TUNING_CONTEXT_PACK_DEFINITIONS_PER_NAME" envDefault:"2" yaml:"definitionsPerName,omitempty"`
	CallersPerSymbol   int `env:"KRITIKA_TUNING_CONTEXT_PACK_CALLERS_PER_SYMBOL" envDefault:"5" yaml:"callersPerSymbol,omitempty"`
	// MaxDefinitions and MaxCallers cap the definition and caller stages.
	MaxDefinitions int `env:"KRITIKA_TUNING_CONTEXT_PACK_MAX_DEFINITIONS" envDefault:"30" yaml:"maxDefinitions,omitempty"`
	MaxCallers     int `env:"KRITIKA_TUNING_CONTEXT_PACK_MAX_CALLERS" envDefault:"30" yaml:"maxCallers,omitempty"`
	// MaxChunkBytes truncates one chunk.
	MaxChunkBytes ByteSize `env:"KRITIKA_TUNING_CONTEXT_PACK_MAX_CHUNK_BYTES" envDefault:"6000" yaml:"maxChunkBytes,omitempty"`
	// MaxPackBytes stops adding chunks: the default fits a budget of about
	// 24k tokens with room for the diff.
	MaxPackBytes ByteSize `env:"KRITIKA_TUNING_CONTEXT_PACK_MAX_PACK_BYTES" envDefault:"300000" yaml:"maxPackBytes,omitempty"`
	// MaxScanFiles and MaxScanBytes bound the head tree's walk.
	MaxScanFiles int      `env:"KRITIKA_TUNING_CONTEXT_PACK_MAX_SCAN_FILES" envDefault:"20000" yaml:"maxScanFiles,omitempty"`
	MaxScanBytes ByteSize `env:"KRITIKA_TUNING_CONTEXT_PACK_MAX_SCAN_BYTES" envDefault:"200Mi" yaml:"maxScanBytes,omitempty"`
	// MaxFileBytes skips files larger than this everywhere.
	MaxFileBytes ByteSize `env:"KRITIKA_TUNING_CONTEXT_PACK_MAX_FILE_BYTES" envDefault:"1Mi" yaml:"maxFileBytes,omitempty"`
}

// Indexer holds the tunable values of package indexer: the defaults of an
// index build's Options, and the bounds of a diff's similar-code queries.
// The chunks that a repository's index holds depend on them, so a change
// takes full effect as each repository is indexed again.
type Indexer struct {
	// MaxDeclLines is the largest declaration stored whole. Larger ones
	// are represented by their nested declarations, or windowed.
	MaxDeclLines int `env:"KRITIKA_TUNING_INDEXER_MAX_DECL_LINES" envDefault:"150" yaml:"maxDeclLines,omitempty"`
	// WindowLines and WindowOverlap cut files that have no declarations
	// into overlapping windows of lines. The overlap is less than a window.
	WindowLines   int `env:"KRITIKA_TUNING_INDEXER_WINDOW_LINES" envDefault:"60" yaml:"windowLines,omitempty"`
	WindowOverlap int `env:"KRITIKA_TUNING_INDEXER_WINDOW_OVERLAP" envDefault:"10" yaml:"windowOverlap,omitempty" tuning:"zero"`
	// MaxChunkBytes truncates one chunk's text.
	MaxChunkBytes ByteSize `env:"KRITIKA_TUNING_INDEXER_MAX_CHUNK_BYTES" envDefault:"6000" yaml:"maxChunkBytes,omitempty"`
	// MaxFileBytes skips larger files, in a build and in a diff's queries.
	MaxFileBytes ByteSize `env:"KRITIKA_TUNING_INDEXER_MAX_FILE_BYTES" envDefault:"1Mi" yaml:"maxFileBytes,omitempty"`
	// MaxFiles, MaxBytes and MaxChunks stop the walk.
	MaxFiles  int      `env:"KRITIKA_TUNING_INDEXER_MAX_FILES" envDefault:"50000" yaml:"maxFiles,omitempty"`
	MaxBytes  ByteSize `env:"KRITIKA_TUNING_INDEXER_MAX_BYTES" envDefault:"500Mi" yaml:"maxBytes,omitempty"`
	MaxChunks int      `env:"KRITIKA_TUNING_INDEXER_MAX_CHUNKS" envDefault:"100000" yaml:"maxChunks,omitempty"`
	// MaxSignatureBytes bounds a chunk's signature, the line that names a
	// windowed declaration, which a minified or generated declaration can
	// make one long line.
	MaxSignatureBytes ByteSize `env:"KRITIKA_TUNING_INDEXER_MAX_SIGNATURE_BYTES" envDefault:"200" yaml:"maxSignatureBytes,omitempty"`
	// MinGapLines is the fewest worded lines that a gap between an
	// oversized declaration's nested ones needs to be stored by itself. A
	// smaller gap, such as a field or a class's opening line, joins the
	// nested declaration next to it when the two fit one piece.
	MinGapLines int `env:"KRITIKA_TUNING_INDEXER_MIN_GAP_LINES" envDefault:"3" yaml:"minGapLines,omitempty"`
	// QueryContext is how many unchanged lines a similar-code query keeps
	// on each side of a hunk's changed lines.
	QueryContext int `env:"KRITIKA_TUNING_INDEXER_QUERY_CONTEXT" envDefault:"3" yaml:"queryContext,omitempty" tuning:"zero"`
}

// Chunk holds the tunable values of package chunk. A parse has no
// timeout: every limit on it counts the parser's work, not time, so that a
// file is cut the same on a loaded machine as on an idle one.
type Chunk struct {
	// MaxFileBytes is the largest file parsed when the caller sets no
	// other limit.
	MaxFileBytes ByteSize `env:"KRITIKA_TUNING_CHUNK_MAX_FILE_BYTES" envDefault:"1Mi" yaml:"maxFileBytes,omitempty"`
	// ParseMemoryBudget bounds one parse's arena and scratch memory. The
	// library's default, the larger of 512 MiB and 512 bytes per input
	// byte, is past a runner pod's memory limit for a file of the size
	// that maxFileBytes lets through. A parse that it stops falls back to
	// fixed windows.
	ParseMemoryBudget ByteSize `env:"KRITIKA_TUNING_CHUNK_PARSE_MEMORY_BUDGET" envDefault:"256Mi" yaml:"parseMemoryBudget,omitempty"`
	// ParseStackDepth stops a parse whose stack runs deep, as a long run of
	// open brackets drives it: the library recovers at the end of such a
	// file for minutes otherwise.
	ParseStackDepth int `env:"KRITIKA_TUNING_CHUNK_PARSE_STACK_DEPTH" envDefault:"4096" yaml:"parseStackDepth,omitempty"`
}

// Lexical holds the tunable values of package lexical: bounds on what a
// document of the search by name stores. A longer run of letters is a key,
// a hash or encoded data, which no query names.
type Lexical struct {
	// MaxDocumentLexemes is how many lexemes one document keeps.
	MaxDocumentLexemes int `env:"KRITIKA_TUNING_LEXICAL_MAX_DOCUMENT_LEXEMES" envDefault:"2048" yaml:"maxDocumentLexemes,omitempty"`
	// MaxLexemeBytes is the longest lexeme kept, at most the 2047 bytes of
	// a lexeme of a PostgreSQL tsvector.
	MaxLexemeBytes int `env:"KRITIKA_TUNING_LEXICAL_MAX_LEXEME_BYTES" envDefault:"64" yaml:"maxLexemeBytes,omitempty"`
}

// postgresMaxLexemeBytes is the longest lexeme that a PostgreSQL tsvector
// holds: array_to_tsvector refuses a longer one, and with it the batch of
// chunks that holds it.
const postgresMaxLexemeBytes = 2047

func init() {
	crossField = append(crossField,
		func(v Values) error {
			return exceeds("indexer.windowLines", v.Indexer.WindowLines, "indexer.windowOverlap", v.Indexer.WindowOverlap)
		},
		func(v Values) error {
			if v.Lexical.MaxLexemeBytes > postgresMaxLexemeBytes {
				return fmt.Errorf("%s (%d) %w PostgreSQL's tsvector lexeme limit, %d bytes",
					fieldLabel("lexical.maxLexemeBytes"), v.Lexical.MaxLexemeBytes, errMustNotExceed, postgresMaxLexemeBytes)
			}
			return nil
		},
		func(v Values) error {
			return notAbove("gateway.similarMax", v.Gateway.SimilarMax, "contextPack.similarLimit", v.ContextPack.SimilarLimit)
		},
	)
}
