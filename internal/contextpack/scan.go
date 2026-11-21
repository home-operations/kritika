package contextpack

import (
	"bytes"
	"context"
	"slices"

	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/storer"

	"github.com/home-operations/kritika/internal/chunk"
)

// scanParts searches each parseable head file once and parses only files
// containing identifiers wanted by a part. Builders share trees and options,
// but retain their own candidate selection and output budgets.
func scanParts(ctx context.Context, builders []*builder, stats *Stats) error {
	type lookup struct {
		builder *builder
		changed bool
	}
	wanted := map[string][]lookup{}
	for _, b := range builders {
		b.definitions, b.callers = map[string]*candidates{}, map[string]*candidates{}
		for _, id := range b.identifiers {
			wanted[id] = append(wanted[id], lookup{builder: b})
		}
		for _, id := range b.changedSymbols {
			wanted[id] = append(wanted[id], lookup{builder: b, changed: true})
		}
	}
	if len(wanted) == 0 || builders[0].in.Head == nil {
		return ctx.Err()
	}
	b := builders[0]
	iter := b.in.Head.Files()
	defer iter.Close()
	return iter.ForEach(func(f *object.File) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if stats.FilesScanned >= b.opts.MaxScanFiles || stats.BytesScanned >= b.opts.MaxScanBytes {
			stats.ScanTruncated = true
			return storer.ErrStop
		}
		if f.Size > int64(b.opts.MaxFileBytes) || b.ignored(f.Name) {
			return nil
		}
		if _, decls := chunk.Language(f.Name); !decls {
			return nil
		}
		src := b.readFile(f)
		if src == nil {
			return nil
		}
		stats.FilesScanned++
		stats.BytesScanned += len(src)
		var hits []hit
		line := 1
		last := 0
		for i := 0; i < len(src); {
			if !wordStart(src[i]) {
				i++
				continue
			}
			j := i + 1
			for j < len(src) && (wordStart(src[j]) || '0' <= src[j] && src[j] <= '9') {
				j++
			}
			if len(wanted[string(src[i:j])]) > 0 {
				line += bytes.Count(src[last:i], []byte{'\n'})
				last = i
				hits = append(hits, hit{string(src[i:j]), line})
			}
			i = j
		}
		if len(hits) == 0 {
			return nil
		}
		pf := b.parser.Parse(f.Name, src)
		defer pf.Close()
		stats.FilesParsed++
		if len(pf.Decls) == 0 {
			return nil
		}
		seenDecl := map[string]bool{}
		for i, decl := range users(pf, hits, b.opts.MaxDeclLines) {
			h := hits[i]
			if decl == nil {
				continue
			}
			key := h.word + "\x00" + f.Name + "\x00" + decl.Symbol
			if seenDecl[key] {
				continue
			}
			seenDecl[key] = true
			for _, target := range wanted[h.word] {
				if !target.builder.overlayDecls[f.Name+"\x00"+decl.Symbol] {
					target.builder.addCandidate(pf, decl, h.line, h.word, target.changed)
				}
			}
		}
		return nil
	})
}

// hit is a wanted word that a file's text holds, and its line.
type hit struct {
	word string
	line int
}

// users is, for each hit, the declaration that uses its word, or nil: the
// smallest one around its line that fits maxLines. A word in the comments
// above a declaration does not use it, as one in a decorator or an
// attribute there does.
func users(pf *chunk.File, hits []hit, maxLines int) []*chunk.Decl {
	decls := make([]*chunk.Decl, len(hits))
	var above []int
	for i, h := range hits {
		decls[i] = pf.Enclosing([]int{h.line}, maxLines)
		if decls[i] != nil && h.line < decls[i].HeadLine {
			above = append(above, h.line)
		}
	}
	named := pf.LineIdentifiers(above)
	for i, h := range hits {
		if d := decls[i]; d != nil && h.line < d.HeadLine && !slices.Contains(named[h.line], h.word) {
			decls[i] = nil
		}
	}
	return decls
}
