package tuning

import (
	"fmt"
	"math"
	"time"

	"github.com/home-operations/kritika/internal/textcut"
)

// Gateway holds the tunable values of package gateway.
type Gateway struct {
	// SimilarPerQuery is how many neighbours one query of a similar-code
	// request keeps, unless a search of one query asks for more.
	SimilarPerQuery int `env:"KRITIKA_TUNING_GATEWAY_SIMILAR_PER_QUERY" envDefault:"4" yaml:"similarPerQuery,omitempty"`
	// SimilarPerFile is how many chunks of one file an answer keeps, so
	// that one file's neighbouring declarations do not take every place.
	SimilarPerFile int `env:"KRITIKA_TUNING_GATEWAY_SIMILAR_PER_FILE" envDefault:"2" yaml:"similarPerFile,omitempty"`
	// SimilarMax is how many chunks an answer keeps when its request asks
	// for no other number.
	SimilarMax int `env:"KRITIKA_TUNING_GATEWAY_SIMILAR_MAX" envDefault:"10" yaml:"similarMax,omitempty"`

	// Drain is how long a stopping gateway lets model steps in flight
	// finish. A step that it cuts is paid for and not recorded, and the
	// runner's retry is paid for again. So Drain covers a long step, not
	// the usual few seconds. The chart's grace period must outlast
	// process.linger plus it.
	Drain time.Duration `env:"KRITIKA_TUNING_GATEWAY_DRAIN" envDefault:"2m" yaml:"drain,omitempty"`
	// MaxConversationBytes bounds one model step's request, the whole
	// conversation so far with every tool output in it capped, and so the
	// runner's read of the conversation that it carries on.
	MaxConversationBytes ByteSize `env:"KRITIKA_TUNING_GATEWAY_MAX_CONVERSATION_BYTES" envDefault:"16Mi" yaml:"maxConversationBytes,omitempty"`
	// DetachTimeout bounds the writes that settle a step once the provider
	// answers. These writes must land even if the request's context ends.
	DetachTimeout time.Duration `env:"KRITIKA_TUNING_GATEWAY_DETACH_TIMEOUT" envDefault:"2m" yaml:"detachTimeout,omitempty"`
	// RefusedWriteTimeout bounds the write that records a refused proxy
	// destination, so that a looping agent's refusals are answered at the
	// pace of the database and no slower.
	RefusedWriteTimeout time.Duration `env:"KRITIKA_TUNING_GATEWAY_REFUSED_WRITE_TIMEOUT" envDefault:"5s" yaml:"refusedWriteTimeout,omitempty"`

	// MaxReportBody bounds a run's context, agent and similar-exclusion
	// reports, as JSON encodes them. A context report holds its pack's two
	// diffs, each of up to runner.maxPackDiffBytes, which escaping can make
	// six times as long; the runner leaves whole files out of them until
	// the report fits. An agent report holds the conversation, of up to
	// maxConversationBytes, in base64.
	MaxReportBody ByteSize `env:"KRITIKA_TUNING_GATEWAY_MAX_REPORT_BODY" envDefault:"64Mi" yaml:"maxReportBody,omitempty"`
	// MaxCheckpointBody bounds a part checkpoint, which holds one part's
	// submitted answer, far below it. A checkpoint that it refuses only
	// means a retry runs that part again.
	MaxCheckpointBody ByteSize `env:"KRITIKA_TUNING_GATEWAY_MAX_CHECKPOINT_BODY" envDefault:"4Mi" yaml:"maxCheckpointBody,omitempty"`
	// MaxSmallReport bounds a run's phase and failure reports, as JSON
	// encodes them: a failure report holds a cause of up to
	// runner.maxFailureCause, which escaping can make six times as long, and
	// the runner cuts a cause further to fit.
	MaxSmallReport ByteSize `env:"KRITIKA_TUNING_GATEWAY_MAX_SMALL_REPORT" envDefault:"64Ki" yaml:"maxSmallReport,omitempty"`
	// MaxPartsReport bounds a split review's remaining-parts report, which
	// names each of its slot pools twice.
	MaxPartsReport ByteSize `env:"KRITIKA_TUNING_GATEWAY_MAX_PARTS_REPORT" envDefault:"64Ki" yaml:"maxPartsReport,omitempty"`
	// MaxIndexBody bounds an index upload: its opening line, and a line for
	// each of up to indexer.maxChunks chunks of indexer.maxChunkBytes, whose
	// source text the upload's JSON can make twice as long. Control bytes or
	// invalid UTF-8 can make it longer, which the runner checks before it
	// uploads.
	MaxIndexBody ByteSize `env:"KRITIKA_TUNING_GATEWAY_MAX_INDEX_BODY" envDefault:"2Gi" yaml:"maxIndexBody,omitempty"`
	// MaxIndexLine bounds one line of an index upload: a chunk, its text
	// escaped at worst, and the opening line, which holds every changed path.
	MaxIndexLine ByteSize `env:"KRITIKA_TUNING_GATEWAY_MAX_INDEX_LINE" envDefault:"32Mi" yaml:"maxIndexLine,omitempty"`
	// IndexBatchChunks and IndexBatchBytes are when an index upload's
	// batch is written: as soon as it holds either, so an upload costs a
	// batch and a line of memory however long it is.
	IndexBatchChunks int      `env:"KRITIKA_TUNING_GATEWAY_INDEX_BATCH_CHUNKS" envDefault:"500" yaml:"indexBatchChunks,omitempty"`
	IndexBatchBytes  ByteSize `env:"KRITIKA_TUNING_GATEWAY_INDEX_BATCH_BYTES" envDefault:"8Mi" yaml:"indexBatchBytes,omitempty"`

	// AquaRegistryTimeout bounds one fetch of the aqua registry that the
	// gateway keeps for runners.
	AquaRegistryTimeout time.Duration `env:"KRITIKA_TUNING_GATEWAY_AQUA_REGISTRY_TIMEOUT" envDefault:"2m" yaml:"aquaRegistryTimeout,omitempty"`
	// AquaRegistryRetry is how soon a replica that has no registry yet asks
	// again. So a replica that started while its source was down serves
	// tools soon after the source is back.
	AquaRegistryRetry time.Duration `env:"KRITIKA_TUNING_GATEWAY_AQUA_REGISTRY_RETRY" envDefault:"1m" yaml:"aquaRegistryRetry,omitempty"`

	// MaxSimilarRequest bounds a similar-code request as JSON encodes it:
	// its queries, each cut to contextPack.similarQueryChars, which escaping
	// can make six times as long, and their languages. The paths a search
	// leaves out are the run's report, not the request's, so the bound
	// holds however many files a change touches.
	MaxSimilarRequest ByteSize `env:"KRITIKA_TUNING_GATEWAY_MAX_SIMILAR_REQUEST" envDefault:"256Ki" yaml:"maxSimilarRequest,omitempty"`
	// SimilarWithin is how long after a similar-code request arrives
	// the vector search's looks past full files, its search by name and its
	// reranking end at the latest, leaving the hits found until then, in
	// the fused order, to the queries that they have not reached. A run
	// waits runner.similarTimeout for its answer from before it sends the
	// request, whatever the embedding and the searches took of it, so this
	// is less. The rest is for the answer, its accounting, and the
	// request's way to the gateway.
	SimilarWithin time.Duration `env:"KRITIKA_TUNING_GATEWAY_SIMILAR_WITHIN" envDefault:"50s" yaml:"similarWithin,omitempty"`
	// RRFK is reciprocal rank fusion's constant: a candidate's score in its
	// query is the sum, over the searches that found it, of 1 / (rrfK +
	// its rank in that search's answer), counted from 1. The default is
	// the value of the paper that introduced the fusion (Cormack, Clarke
	// and Buettcher, 2009), which damps the weight of the very top ranks.
	RRFK int `env:"KRITIKA_TUNING_GATEWAY_RRF_K" envDefault:"60" yaml:"rrfK,omitempty"`
	// LexicalShare is the least share of its query's best lexical weight
	// that a hit needs to count as similar code by its names alone.
	LexicalShare float64 `env:"KRITIKA_TUNING_GATEWAY_LEXICAL_SHARE" envDefault:"0.5" yaml:"lexicalShare,omitempty"`
	// SimilarNames is how many of the names a chunk shares with its query
	// its label shows.
	SimilarNames int `env:"KRITIKA_TUNING_GATEWAY_SIMILAR_NAMES" envDefault:"3" yaml:"similarNames,omitempty"`
	// LexicalTimeout bounds the search by name, which a request can do
	// without: its first statement and its looks past full files together,
	// each statement's timeout what is left of it.
	LexicalTimeout time.Duration `env:"KRITIKA_TUNING_GATEWAY_LEXICAL_TIMEOUT" envDefault:"5s" yaml:"lexicalTimeout,omitempty"`
	// LexicalTerms is how many words of a query are searched for, the
	// longest first.
	LexicalTerms int `env:"KRITIKA_TUNING_GATEWAY_LEXICAL_TERMS" envDefault:"32" yaml:"lexicalTerms,omitempty"`
	// LexicalCommonPer, LexicalCommonLeast and LexicalCommonShare bar the
	// words too common to tell a generation's chunks apart: a word in a
	// LexicalCommonPer-th of them, rounded down, or more is left out of the
	// search, the cutoff raised to LexicalCommonLeast when it is less and
	// lowered to store.LexicalCommonMax when it is more, and so is a word in
	// more than 1/LexicalCommonShare of them. The share bars, in a small
	// generation, a word that nearly all of its chunks hold: its weight is
	// too small to say anything of a chunk, yet it would be its query's
	// best by name, and every chunk holding it would clear lexicalShare.
	LexicalCommonPer   int `env:"KRITIKA_TUNING_GATEWAY_LEXICAL_COMMON_PER" envDefault:"100" yaml:"lexicalCommonPer,omitempty"`
	LexicalCommonLeast int `env:"KRITIKA_TUNING_GATEWAY_LEXICAL_COMMON_LEAST" envDefault:"20" yaml:"lexicalCommonLeast,omitempty"`
	LexicalCommonShare int `env:"KRITIKA_TUNING_GATEWAY_LEXICAL_COMMON_SHARE" envDefault:"2" yaml:"lexicalCommonShare,omitempty"`
}

// SimilarDepth is how many chunks each search finds for a query, without a
// reranker, for the fusion to rank: twice what a query keeps, so that the
// places of chunks that the file cap or a duplicate turns away go to the
// query's next ones.
func (g Gateway) SimilarDepth() int { return 2 * g.SimilarPerQuery }

// Egress holds the tunable values of package egress.
type Egress struct {
	// ConnectTimeout bounds opening a connection to a destination.
	ConnectTimeout time.Duration `env:"KRITIKA_TUNING_EGRESS_CONNECT_TIMEOUT" envDefault:"15s" yaml:"connectTimeout,omitempty"`
	// TunnelIdle ends a CONNECT tunnel that neither side used for this
	// long.
	TunnelIdle time.Duration `env:"KRITIKA_TUNING_EGRESS_TUNNEL_IDLE" envDefault:"5m" yaml:"tunnelIdle,omitempty"`
	// TunnelBuffer is the buffer that each direction of a tunnel copies
	// through.
	TunnelBuffer ByteSize `env:"KRITIKA_TUNING_EGRESS_TUNNEL_BUFFER" envDefault:"32Ki" yaml:"tunnelBuffer,omitempty"`
	// UpgradeTimeout bounds one upgraded request, response body included,
	// and one cache fill. A partial cache file twice this old is a fill
	// that died, and is swept.
	UpgradeTimeout time.Duration `env:"KRITIKA_TUNING_EGRESS_UPGRADE_TIMEOUT" envDefault:"2m" yaml:"upgradeTimeout,omitempty"`
	// MaxEntryHeader bounds a cache entry's first line.
	MaxEntryHeader ByteSize `env:"KRITIKA_TUNING_EGRESS_MAX_ENTRY_HEADER" envDefault:"16Ki" yaml:"maxEntryHeader,omitempty"`
	// MaxRedirects bounds the redirects one cache fill follows.
	MaxRedirects int `env:"KRITIKA_TUNING_EGRESS_MAX_REDIRECTS" envDefault:"10" yaml:"maxRedirects,omitempty"`
	// MaxFills bounds the cache fills under way at once, and each fill
	// holds an upstream connection. A request past them is forwarded. A
	// shared S3 cache sizes its local spool to this many entries.
	MaxFills int `env:"KRITIKA_TUNING_EGRESS_MAX_FILLS" envDefault:"8" yaml:"maxFills,omitempty"`

	// S3LookupTimeout bounds each of the S3 cache's lookups: dialling, the
	// TLS handshake, a response's headers, a credential's discovery, and an
	// entry's stat.
	S3LookupTimeout time.Duration `env:"KRITIKA_TUNING_EGRESS_S3_LOOKUP_TIMEOUT" envDefault:"2s" yaml:"s3LookupTimeout,omitempty"`
	// S3KeepAlive is the keep-alive period of the S3 cache's connections.
	S3KeepAlive time.Duration `env:"KRITIKA_TUNING_EGRESS_S3_KEEP_ALIVE" envDefault:"30s" yaml:"s3KeepAlive,omitempty"`
	// S3ReadTimeout bounds reading an entry from the S3 cache.
	S3ReadTimeout time.Duration `env:"KRITIKA_TUNING_EGRESS_S3_READ_TIMEOUT" envDefault:"10s" yaml:"s3ReadTimeout,omitempty"`
	// S3UploadTimeout is the fixed part of an upload's deadline, which
	// S3UploadRate extends by the entry's size.
	S3UploadTimeout time.Duration `env:"KRITIKA_TUNING_EGRESS_S3_UPLOAD_TIMEOUT" envDefault:"30s" yaml:"s3UploadTimeout,omitempty"`
	// S3UploadRate is the slowest upload allowed past the fixed budget, in
	// bytes a second: at the default, a 256Mi entry has about five minutes
	// and the 5Gi cap about ninety, instead of a deadline that no large
	// entry can meet.
	S3UploadRate ByteSize `env:"KRITIKA_TUNING_EGRESS_S3_UPLOAD_RATE" envDefault:"1Mi" yaml:"s3UploadRate,omitempty"`
	// S3Failures is how many failures in a row open a circuit of the S3
	// cache, which then serves from the origin alone for S3Cooldown before
	// it probes again.
	S3Failures int           `env:"KRITIKA_TUNING_EGRESS_S3_FAILURES" envDefault:"3" yaml:"s3Failures,omitempty"`
	S3Cooldown time.Duration `env:"KRITIKA_TUNING_EGRESS_S3_COOLDOWN" envDefault:"30s" yaml:"s3Cooldown,omitempty"`
	// S3UploadWorkers is how many uploads to the S3 cache run at once, and
	// S3UploadQueue how many more wait. An upload past them is skipped.
	S3UploadWorkers int `env:"KRITIKA_TUNING_EGRESS_S3_UPLOAD_WORKERS" envDefault:"2" yaml:"s3UploadWorkers,omitempty"`
	S3UploadQueue   int `env:"KRITIKA_TUNING_EGRESS_S3_UPLOAD_QUEUE" envDefault:"16" yaml:"s3UploadQueue,omitempty"`
}

// S3UploadDeadline is how long an upload of size bytes to the S3 cache can
// take: S3UploadTimeout, and a second for each S3UploadRate bytes.
func (e Egress) S3UploadDeadline(size int64) time.Duration {
	return e.S3UploadTimeout + time.Duration(size/int64(e.S3UploadRate))*time.Second
}

// Aqua holds the tunable values of package aqua.
type Aqua struct {
	// MaxRegistryBytes bounds a fetch of the aqua registry, by the gateway
	// from its source and by a runner from the gateway: many times its
	// size.
	MaxRegistryBytes ByteSize `env:"KRITIKA_TUNING_AQUA_MAX_REGISTRY_BYTES" envDefault:"64Mi" yaml:"maxRegistryBytes,omitempty"`
	// MaxDownloadBytes bounds a package's download, and a minisign
	// signature's message: the largest that the registry's tools ship is a
	// few hundred MiB.
	MaxDownloadBytes ByteSize `env:"KRITIKA_TUNING_AQUA_MAX_DOWNLOAD_BYTES" envDefault:"1Gi" yaml:"maxDownloadBytes,omitempty"`
	// MaxChecksumFileBytes bounds a checksum file, which lists a release's
	// assets.
	MaxChecksumFileBytes ByteSize `env:"KRITIKA_TUNING_AQUA_MAX_CHECKSUM_FILE_BYTES" envDefault:"4Mi" yaml:"maxChecksumFileBytes,omitempty"`
	// MaxReleaseBytes bounds the GitHub API's description of a release.
	MaxReleaseBytes ByteSize `env:"KRITIKA_TUNING_AQUA_MAX_RELEASE_BYTES" envDefault:"16Mi" yaml:"maxReleaseBytes,omitempty"`
	// MaxUnpackedBytes bounds what a download decompresses to, the entries
	// it leaves out and its headers included, against an archive that
	// expands without end.
	MaxUnpackedBytes ByteSize `env:"KRITIKA_TUNING_AQUA_MAX_UNPACKED_BYTES" envDefault:"4Gi" yaml:"maxUnpackedBytes,omitempty"`
	// MaxEntries bounds how many entries an unpacked archive has.
	MaxEntries int `env:"KRITIKA_TUNING_AQUA_MAX_ENTRIES" envDefault:"200000" yaml:"maxEntries,omitempty"`
	// MaxSignatureBytes bounds what a signature is read from: a bundle, a
	// minisign signature, or the GitHub API's attestations of a download,
	// and what a compressed one decodes to.
	MaxSignatureBytes ByteSize `env:"KRITIKA_TUNING_AQUA_MAX_SIGNATURE_BYTES" envDefault:"16Mi" yaml:"maxSignatureBytes,omitempty"`
}

// How a report's JSON grows what it carries, for the bounds that the
// gateway's request limits must hold.
const (
	// jsonGrowth is textcut.JSONGrowth, how much JSON can grow a string.
	jsonGrowth = textcut.JSONGrowth
	// sourceGrowth is what an index upload's encoding, which leaves HTML
	// unescaped, makes of source text at most, but for control bytes and
	// invalid UTF-8, which the indexer's text rarely holds and the runner
	// measures before it uploads: a newline, a tab, a quote or a backslash
	// takes two bytes.
	sourceGrowth = 2
	// indexChunkFields is the room of an index chunk's line beside its text
	// and signature: its keys, its line numbers and newline, and its path,
	// language, symbol, kind and scope.
	indexChunkFields = 2 << 10
	// reportEnvelope is the room of a context or agent report beside the
	// fields whose bounds are tunable.
	reportEnvelope = 1 << 20
	// similarLanguageBytes is the room of one query's language, a name of
	// package chunk's, in a similar-code request, and similarEnvelope that
	// of its keys, its kind and its limit, or of an answer's.
	similarLanguageBytes = 64
	similarEnvelope      = 256
	// smallReportEnvelope is the room of a small report beside a failure
	// report's cause: a phase report whole, or a failure report's key.
	smallReportEnvelope = 1 << 10
)

// satAdd and satMul are a+b and a*b of sizes that cannot be negative,
// capped at math.MaxInt64 where they would overflow.
func satAdd(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

func satMul(a, b int64) int64 {
	if a > 0 && b > math.MaxInt64/a {
		return math.MaxInt64
	}
	return a * b
}

// indexChunkLine is the most that one chunk's line of an index upload
// takes, its text and signature escaped at worst.
func (v Values) indexChunkLine() int64 {
	return satMul(jsonGrowth, satAdd(satAdd(int64(v.Indexer.MaxChunkBytes), int64(v.Indexer.MaxSignatureBytes)), indexChunkFields))
}

// indexUpload is what an index upload of indexer.maxChunks full chunks
// takes: its opening line, which gateway.maxIndexLine bounds, and each
// chunk's line, its source text as the upload encodes it.
func (v Values) indexUpload() int64 {
	line := satAdd(satMul(sourceGrowth, satAdd(int64(v.Indexer.MaxChunkBytes), int64(v.Indexer.MaxSignatureBytes))), indexChunkFields)
	return satAdd(int64(v.Gateway.MaxIndexLine), satMul(int64(v.Indexer.MaxChunks), line))
}

func init() {
	crossField = append(crossField,
		func(v Values) error {
			if v.Gateway.SimilarWithin >= v.Runner.SimilarTimeout {
				return fmt.Errorf("%s (%s) must be less than %s (%s), which a run waits for the answer",
					fieldLabel("gateway.similarWithin"), v.Gateway.SimilarWithin,
					fieldLabel("runner.similarTimeout"), v.Runner.SimilarTimeout)
			}
			return nil
		},
		func(v Values) error {
			if need := v.indexUpload(); int64(v.Gateway.MaxIndexBody) < need {
				return fmt.Errorf("%s (%s) must hold an index upload's opening line of up to %s and %s chunks of %s and %s, "+
					"as the upload encodes them, %d bytes",
					fieldLabel("gateway.maxIndexBody"), v.Gateway.MaxIndexBody, fieldLabel("gateway.maxIndexLine"),
					fieldLabel("indexer.maxChunks"), fieldLabel("indexer.maxChunkBytes"), fieldLabel("indexer.maxSignatureBytes"), need)
			}
			return nil
		},
		func(v Values) error {
			if need := v.indexChunkLine(); int64(v.Gateway.MaxIndexLine) < need {
				return fmt.Errorf("%s (%s) must hold one chunk's line, a text of %s and a signature of %s escaped, %d bytes",
					fieldLabel("gateway.maxIndexLine"), v.Gateway.MaxIndexLine,
					fieldLabel("indexer.maxChunkBytes"), fieldLabel("indexer.maxSignatureBytes"), need)
			}
			return nil
		},
		func(v Values) error {
			diffs := satMul(2, int64(v.Runner.MaxPackDiffBytes))
			need := satAdd(satMul(jsonGrowth, satAdd(diffs, int64(v.ContextPack.MaxPackBytes))), reportEnvelope)
			if int64(v.Gateway.MaxReportBody) < need {
				return fmt.Errorf("%s (%s) must hold a context report: two diffs of %s and a pack of %s, escaped, %d bytes",
					fieldLabel("gateway.maxReportBody"), v.Gateway.MaxReportBody,
					fieldLabel("runner.maxPackDiffBytes"), fieldLabel("contextPack.maxPackBytes"), need)
			}
			return nil
		},
		func(v Values) error {
			conversation := satMul(4, (int64(v.Gateway.MaxConversationBytes)+2)/3)
			if need := satAdd(conversation, reportEnvelope); int64(v.Gateway.MaxReportBody) < need {
				return fmt.Errorf("%s (%s) must hold an agent report: a conversation of %s in base64, %d bytes",
					fieldLabel("gateway.maxReportBody"), v.Gateway.MaxReportBody,
					fieldLabel("gateway.maxConversationBytes"), need)
			}
			return nil
		},
		func(v Values) error {
			if need := satAdd(satMul(jsonGrowth, int64(v.Runner.MaxFailureCause)), smallReportEnvelope); int64(v.Gateway.MaxSmallReport) < need {
				return fmt.Errorf("%s (%s) must hold a phase report and a failure report of %s, escaped, %d bytes",
					fieldLabel("gateway.maxSmallReport"), v.Gateway.MaxSmallReport, fieldLabel("runner.maxFailureCause"), need)
			}
			return nil
		},
		func(v Values) error {
			query := satAdd(satMul(jsonGrowth, int64(v.ContextPack.SimilarQueryChars)), similarLanguageBytes+6)
			need := satAdd(satMul(int64(v.ContextPack.SimilarQueries), query), similarEnvelope)
			if int64(v.Gateway.MaxSimilarRequest) < need {
				return fmt.Errorf("%s (%s) must hold %s queries of %s, each with its language, escaped, %d bytes",
					fieldLabel("gateway.maxSimilarRequest"), v.Gateway.MaxSimilarRequest,
					fieldLabel("contextPack.similarQueries"),
					fieldLabel("contextPack.similarQueryChars"), need)
			}
			return nil
		},
	)
}
