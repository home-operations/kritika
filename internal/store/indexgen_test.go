package store

import "testing"

func TestEmbedText(t *testing.T) {
	tests := []struct {
		name, prompt string
		chunk        StagedChunk
		want         string
	}{
		{name: "with a symbol", chunk: StagedChunk{Path: "a/b.go", Symbol: "Build", Kind: "function", Text: "func Build() {}"},
			want: "a/b.go function Build\nfunc Build() {}"},
		{name: "with a scope", chunk: StagedChunk{Path: "a/b.py", Symbol: "bar", Kind: "function", Scope: "Foo", Text: "def bar(self): pass"},
			want: "a/b.py function Foo.bar\ndef bar(self): pass"},
		{name: "a window with a signature", chunk: StagedChunk{Path: "a/b.py", Symbol: "bar", Kind: "function", Scope: "Foo",
			Signature: "def bar(self):", Text: "        total += 1"},
			want: "a/b.py function Foo.bar\ndef bar(self):\n        total += 1"},
		{name: "a scope without a symbol", chunk: StagedChunk{Path: "a/b.go", Kind: "import", Scope: "Foo", Text: "import \"x\""},
			want: "a/b.go\nimport \"x\""},
		{name: "without a symbol", chunk: StagedChunk{Path: "values.yaml", Text: "a: 1"}, want: "values.yaml\na: 1"},
		{name: "with a document prompt", prompt: "Represent the document for retrieval: ",
			chunk: StagedChunk{Path: "values.yaml", Text: "a: 1"}, want: "Represent the document for retrieval: values.yaml\na: 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.chunk.EmbedText(tt.prompt); got != tt.want {
				t.Errorf("EmbedText(%q) of %+v = %q, want %q", tt.prompt, tt.chunk, got, tt.want)
			}
		})
	}
}
