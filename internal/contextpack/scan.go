package contextpack

import (
	"bytes"
	"context"

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
		type hit struct {
			word string
			line int
		}
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
		for _, h := range hits {
			decl := pf.Enclosing([]int{h.line}, b.opts.MaxDeclLines)
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
					target.builder.addCandidate(pf, decl, h.word, target.changed)
				}
			}
		}
		return nil
	})
}
