package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/arrase/code-reducer/internal/config"
)

func TestRenderModulePageSkeleton(t *testing.T) {
	page := renderModulePage("internal/engine", modulePageParts{
		responsibility: "Synthesizes module documentation",
		components: []moduleComponentLine{
			{name: "File: client.go", text: "Talks to the Ollama API"},
			{name: "Subsystem: config", text: "Resolves configuration precedence"},
		},
		dataFlow:      "File facts flow up the tree.",
		errorHandling: "Failures abort the page.",
	})

	want := strings.Join([]string{
		"# Module: internal/engine",
		"",
		"## Responsibility",
		"Synthesizes module documentation",
		"",
		"## Components",
		"",
		"### File: client.go",
		"Talks to the Ollama API",
		"",
		"### Subsystem: config",
		"Resolves configuration precedence",
		"",
		"## Data Flow",
		"File facts flow up the tree.",
		"",
		"## Error Handling",
		"Failures abort the page.",
		"",
	}, "\n")

	if page != want {
		t.Fatalf("skeleton mismatch\n got:\n%s\nwant:\n%s", page, want)
	}
}

func TestRenderDocPageSkeleton(t *testing.T) {
	page := renderDocPage("Architecture", []namedSection{
		{heading: "Overview", text: "Generates a wiki."},
		{heading: "System Boundaries", text: "Only talks to Ollama."},
	})

	want := "# Architecture\n\n## Overview\nGenerates a wiki.\n\n## System Boundaries\nOnly talks to Ollama.\n"
	if page != want {
		t.Fatalf("skeleton mismatch\n got:\n%q\nwant:\n%q", page, want)
	}
}

func TestParseModuleComponents(t *testing.T) {
	components := []string{
		"### File: client.go\n#### [API_SIGNATURES]\nfunc Call()",
		"### Subsystem: config\n# Module: internal/config",
		"### File: orphan.go",
	}

	parsed := parseModuleComponents(components)
	if len(parsed) != 3 {
		t.Fatalf("expected 3 components, got %d", len(parsed))
	}
	if parsed[0].name != "File: client.go" || parsed[0].facts != "#### [API_SIGNATURES]\nfunc Call()" {
		t.Errorf("unexpected first component: %+v", parsed[0])
	}
	if parsed[1].name != "Subsystem: config" || parsed[1].facts != "# Module: internal/config" {
		t.Errorf("unexpected second component: %+v", parsed[1])
	}
	if parsed[2].name != "File: orphan.go" || parsed[2].facts != "" {
		t.Errorf("unexpected third component: %+v", parsed[2])
	}
}

func TestSlotKindSalvage(t *testing.T) {
	overLong := truncatedResult("Keeps the first sentence. Drops the rest that never end")

	if got := slotLine.salvage(overLong); got != "Keeps the first sentence." {
		t.Errorf("expected the first complete sentence of a line slot, got %q", got)
	}
	if got := slotParagraph.salvage(overLong); got != "Keeps the first sentence." {
		t.Errorf("expected a short paragraph answer to survive whole, got %q", got)
	}
	if got := slotLine.salvage(stopResult("A complete answer.")); got != "" {
		t.Errorf("expected no salvage for a complete answer, got %q", got)
	}
	if got := slotLine.salvage(truncatedResult("")); got != "" {
		t.Errorf("expected no salvage for an empty answer, got %q", got)
	}
	if got := slotLine.salvage(truncatedResult("Clipped before the first period")); got != "" {
		t.Errorf("expected no salvage without a complete sentence, got %q", got)
	}
}

func TestSlotParagraphSalvageKeepsTextUpToTheLastSentence(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "clipped mid paragraph",
			in:   "Facts flow up the tree. Failures travel back down. The rest of the paragraph is cut off mid",
			want: "Facts flow up the tree. Failures travel back down.",
		},
		{
			name: "abbreviation does not end a sentence",
			in:   "The flags win, e.g. --slot-num-predict, over the file. The file is read last, i.e. from the repo root. And the tail never",
			want: "The flags win, e.g. --slot-num-predict, over the file. The file is read last, i.e. from the repo root.",
		},
		{
			name: "clause punctuation does not end a sentence",
			in:   "The flags win, then the file, and the run starts. The tail of this answer never",
			want: "The flags win, then the file, and the run starts.",
		},
		{
			name: "unterminated fence keeps its opening line",
			in:   "```\nThe flags win. The file is read last. The tail never",
			want: "```\nThe flags win. The file is read last.",
		},
		{name: "no complete sentence", in: "The tail of this answer never ends on a boundar", want: ""},
		{name: "empty answer", in: "   ", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := slotParagraph.salvage(truncatedResult(tc.in)); got != tc.want {
				t.Fatalf("salvage(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSlotParagraphSalvageFeedsSanitizeAWholeParagraph(t *testing.T) {
	res := truncatedResult("```\n- The flags win.\n- The file is read last.\n- The tail never")

	text := slotParagraph.sanitize(slotParagraph.salvage(res))
	if text != "The flags win. The file is read last." {
		t.Fatalf("expected the salvaged prefix to survive normalization, got %q", text)
	}
}

func TestSlotKindSalvagedSpanNamesTheKeptPrefix(t *testing.T) {
	if got := slotLine.salvagedSpan(); got != "kept the first complete sentence" {
		t.Errorf("unexpected line salvage log: %q", got)
	}
	if got := slotParagraph.salvagedSpan(); got != "kept the text up to the last complete sentence" {
		t.Errorf("unexpected paragraph salvage log: %q", got)
	}
}

func TestSanitizeSlotContent(t *testing.T) {
	cases := []struct {
		name string
		kind slotKind
		in   string
		want string
	}{
		{name: "clean line", kind: slotLine, in: "Calls the Ollama API.", want: "Calls the Ollama API"},
		{name: "bullet line", kind: slotLine, in: "- Calls the Ollama API.", want: "Calls the Ollama API"},
		{name: "numbered line", kind: slotLine, in: "1. Calls the Ollama API", want: "Calls the Ollama API"},
		{name: "multi line", kind: slotLine, in: "Calls the Ollama API\nand caches the result", want: "Calls the Ollama API"},
		{name: "bulleted list", kind: slotLine, in: "- Calls the Ollama API\n- Caches the result\n- Streams the errors", want: "Calls the Ollama API"},
		{name: "multiple sentences", kind: slotLine, in: "Calls the Ollama API. Caches the result. Streams the errors.", want: "Calls the Ollama API"},
		{name: "no terminator", kind: slotLine, in: "Calls the Ollama API and caches the result", want: "Calls the Ollama API and caches the result"},
		{name: "abbreviation", kind: slotLine, in: "Resolves the config, e.g. num_ctx, then starts the run", want: "Resolves the config, e.g. num_ctx, then starts the run"},
		{name: "fenced line", kind: slotLine, in: "```go\nCall()\n```", want: "Call()"},
		{name: "heading line", kind: slotLine, in: "## Responsibility\nCalls the Ollama API", want: "Calls the Ollama API"},
		{name: "only formatting", kind: slotLine, in: "- \n- .\n```", want: ""},
		{name: "paragraph", kind: slotParagraph, in: "- first point\n- second point", want: "first point second point"},
		{name: "paragraph with fence", kind: slotParagraph, in: "```\nA wrapped answer.\n```", want: "A wrapped answer."},
		{name: "empty", kind: slotParagraph, in: "   \n\t", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.kind.sanitize(tc.in); got != tc.want {
				t.Fatalf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeSlotLineBoundsWordBudget(t *testing.T) {
	t.Run("cuts at a clause boundary", func(t *testing.T) {
		runOn := strings.TrimSpace(strings.Repeat("alpha ", 24) + "tail, " + strings.Repeat("beta ", 30))
		want := strings.TrimSpace(strings.Repeat("alpha ", 24) + "tail")

		if got := sanitizeSlotLine(runOn); got != want {
			t.Fatalf("expected the cut to land on the clause boundary, got %q", got)
		}
	})

	t.Run("cuts on a word boundary without punctuation", func(t *testing.T) {
		words := strings.TrimSpace(strings.Repeat("gamma ", slotLineWordBudget+20))
		got := sanitizeSlotLine(words)
		if n := len(strings.Fields(got)); n != slotLineWordBudget {
			t.Fatalf("expected %d words, got %d in %q", slotLineWordBudget, n, got)
		}
		for _, w := range strings.Fields(got) {
			if w != "gamma" {
				t.Fatalf("expected whole words only, got %q", w)
			}
		}
	})

	t.Run("keeps a short line whole", func(t *testing.T) {
		short := strings.TrimSpace(strings.Repeat("delta ", 10))
		if got := sanitizeSlotLine(short); got != short {
			t.Fatalf("expected %q, got %q", short, got)
		}
	})
}

func TestBuildModulePageFillsOneSlotPerCall(t *testing.T) {
	client := &scriptedLLM{numCtx: 2048, results: []LLMResult{
		stopResult("Holds every module in the repository"),
		stopResult("Talks to the Ollama API"),
		stopResult("Resolves configuration precedence"),
		stopResult("Facts flow up the tree"),
		stopResult("Failures abort the page"),
	}}
	p := newTestPipeline(t, client, &config.Config{
		DocsDir:               "docs",
		CharsPerToken:         config.CharsPerTokenDefault,
		SlotNumPredict:        96,
		ParagraphNumPredict:   512,
		SystemPrompt:          "system",
		ModuleSynthesisPrompt: "module",
	})

	page, err := buildModulePage(context.Background(), p, "internal", []moduleComponent{
		{name: "File: client.go", facts: "#### [API_SIGNATURES]\nCall()"},
		{name: "Subsystem: config", facts: "#### [API_SIGNATURES]\nResolve()"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if client.calls != 5 {
		t.Fatalf("expected 5 slot calls for 2 components, got %d", client.calls)
	}
	for i, system := range client.systems {
		if system != "system" {
			t.Errorf("call %d: expected the bare system prompt, got %q", i, system)
		}
	}
	for i, bound := range client.bounds {
		want := 96
		if i >= 3 {
			want = 512
		}
		if bound != want {
			t.Errorf("call %d: expected a num_predict of %d, got %d", i, want, bound)
		}
	}
	if !strings.Contains(page, "## Data Flow\nFacts flow up the tree") {
		t.Errorf("expected the data flow slot in the page, got:\n%s", page)
	}

	if !strings.Contains(client.prompts[0], "Components:\n- File: client.go\n- Subsystem: config") {
		t.Errorf("expected the responsibility slot to see the component identities, got:\n%s", client.prompts[0])
	}
	if !strings.HasPrefix(client.prompts[0], "module\n\n") {
		t.Errorf("expected the module style prompt to open the user turn, got:\n%s", client.prompts[0])
	}
	if strings.Contains(client.prompts[0], "Call()") || strings.Contains(client.prompts[0], "Resolve()") {
		t.Errorf("expected the responsibility slot not to receive component facts, got:\n%s", client.prompts[0])
	}
	if !strings.Contains(client.prompts[1], "Call()") || strings.Contains(client.prompts[1], "Resolve()") {
		t.Errorf("expected the component slot to see only its own facts, got:\n%s", client.prompts[1])
	}
}

func TestBuildModulePageFailsOnAnySlot(t *testing.T) {
	cases := []struct {
		name    string
		failing int
		wantErr error
	}{
		{name: "responsibility", failing: 0, wantErr: ErrTruncatedOutput},
		{name: "component", failing: 1, wantErr: ErrTruncatedOutput},
		{name: "data flow", failing: 2, wantErr: ErrEmptyOutput},
		{name: "error handling", failing: 3, wantErr: ErrTruncatedOutput},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			results := []LLMResult{stopResult("ok"), stopResult("ok"), stopResult("ok"), stopResult("ok")}
			switch tc.wantErr {
			case ErrTruncatedOutput:
				results[tc.failing] = truncatedResult("cut off")
			default:
				results[tc.failing] = stopResult("")
			}
			client := &scriptedLLM{numCtx: 2048, results: results}
			p := newTestPipeline(t, client, &config.Config{
				DocsDir:               "docs",
				CharsPerToken:         config.CharsPerTokenDefault,
				SystemPrompt:          "system",
				ModuleSynthesisPrompt: "module",
			})

			page, err := buildModulePage(context.Background(), p, "internal", []moduleComponent{
				{name: "File: client.go", facts: "facts"},
				{name: "File: tree.go", facts: "facts"},
			})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v (page: %q)", tc.wantErr, err, page)
			}
			if page != "" {
				t.Errorf("expected no page when a slot fails, got %q", page)
			}
		})
	}
}

func TestBuildModulePageRejectsUnusableSlotOutput(t *testing.T) {
	client := &scriptedLLM{numCtx: 2048, results: []LLMResult{stopResult("- \n- .\n```")}}
	p := newTestPipeline(t, client, &config.Config{
		DocsDir:               "docs",
		CharsPerToken:         config.CharsPerTokenDefault,
		SystemPrompt:          "system",
		ModuleSynthesisPrompt: "module",
	})

	page, err := buildModulePage(context.Background(), p, "internal", []moduleComponent{{name: "File: client.go", facts: "facts"}})
	if !errors.Is(err, ErrEmptyOutput) {
		t.Fatalf("expected ErrEmptyOutput, got %v (page: %q)", err, page)
	}
}

func TestBuildModulePageSalvagesTruncatedLineSlots(t *testing.T) {
	client := &scriptedLLM{numCtx: 2048, results: []LLMResult{
		truncatedResult("Holds the CLI commands. It also writes a very long tail that the limit cuts off mid"),
		truncatedResult("Registers the cobra commands. It also sets up the flags and"),
		stopResult("Resolves configuration precedence"),
		stopResult("Facts flow up the tree"),
		stopResult("Failures abort the page"),
	}}
	p := newTestPipeline(t, client, &config.Config{
		DocsDir:               "docs",
		CharsPerToken:         config.CharsPerTokenDefault,
		SystemPrompt:          "system",
		ModuleSynthesisPrompt: "module",
	})

	page, err := buildModulePage(context.Background(), p, "cmd", []moduleComponent{
		{name: "File: root.go", facts: "facts"},
		{name: "File: setup.go", facts: "facts"},
	})
	if err != nil {
		t.Fatalf("expected the page to complete, got %v", err)
	}
	if client.calls != 5 {
		t.Fatalf("expected 5 slot calls, got %d", client.calls)
	}
	if !strings.Contains(page, "## Responsibility\nHolds the CLI commands\n") {
		t.Errorf("expected the salvaged responsibility sentence, got:\n%s", page)
	}
	if !strings.Contains(page, "### File: root.go\nRegisters the cobra commands\n") {
		t.Errorf("expected the salvaged component sentence, got:\n%s", page)
	}
	if strings.Contains(page, "cuts off mid") {
		t.Errorf("expected the truncated tail to be dropped, got:\n%s", page)
	}
}

func TestBuildModulePageFailsOnTruncatedLineSlotWithoutSentence(t *testing.T) {
	client := &scriptedLLM{numCtx: 2048, results: []LLMResult{
		truncatedResult("Holds the CLI commands and also writes a very long tail that the"),
	}}
	p := newTestPipeline(t, client, &config.Config{
		DocsDir:               "docs",
		CharsPerToken:         config.CharsPerTokenDefault,
		SystemPrompt:          "system",
		ModuleSynthesisPrompt: "module",
	})

	page, err := buildModulePage(context.Background(), p, "cmd", []moduleComponent{{name: "File: root.go", facts: "facts"}})
	if !errors.Is(err, ErrTruncatedOutput) {
		t.Fatalf("expected ErrTruncatedOutput for a clipped sentence, got %v (page: %q)", err, page)
	}
}

func TestBuildModulePageSalvagesTruncatedParagraphSlot(t *testing.T) {
	client := &scriptedLLM{numCtx: 2048, results: []LLMResult{
		stopResult("Holds the CLI commands"),
		stopResult("Registers the cobra commands"),
		truncatedResult("Facts flow up the tree. Failures travel back down the call chain. The rest of the paragraph is cut off mid"),
	}}
	p := newTestPipeline(t, client, &config.Config{
		DocsDir:               "docs",
		CharsPerToken:         config.CharsPerTokenDefault,
		SystemPrompt:          "system",
		ModuleSynthesisPrompt: "module",
	})

	page, err := buildModulePage(context.Background(), p, "cmd", []moduleComponent{{name: "File: root.go", facts: "facts"}})
	if err != nil {
		t.Fatalf("expected the page to complete, got %v", err)
	}
	if !strings.Contains(page, "## Data Flow\nFacts flow up the tree. Failures travel back down the call chain.\n") {
		t.Errorf("expected the text up to the last complete sentence, got:\n%s", page)
	}
	if strings.Contains(page, "cut off mid") {
		t.Errorf("expected the clipped tail to be dropped, got:\n%s", page)
	}
}

func TestBuildModulePageFailsOnTruncatedParagraphSlotWithoutSentence(t *testing.T) {
	client := &scriptedLLM{numCtx: 2048, results: []LLMResult{
		stopResult("Holds the CLI commands"),
		stopResult("Registers the cobra commands"),
		truncatedResult("Facts flow up the tree and then the rest of the paragraph never ends on a boundar"),
	}}
	p := newTestPipeline(t, client, &config.Config{
		DocsDir:               "docs",
		CharsPerToken:         config.CharsPerTokenDefault,
		SystemPrompt:          "system",
		ModuleSynthesisPrompt: "module",
	})

	page, err := buildModulePage(context.Background(), p, "cmd", []moduleComponent{{name: "File: root.go", facts: "facts"}})
	if !errors.Is(err, ErrTruncatedOutput) {
		t.Fatalf("expected ErrTruncatedOutput for a clipped paragraph without a sentence, got %v (page: %q)", err, page)
	}
	if page != "" {
		t.Errorf("expected no page when a slot fails, got %q", page)
	}
}

func TestBuildModulePageRendersOneBoundedLinePerSlot(t *testing.T) {
	enumeration := strings.Join([]string{
		"- Talks to the Ollama API and streams the result",
		"- Caches every response in the metadata file",
		"- Retries a failed call once",
	}, "\n")
	client := &scriptedLLM{numCtx: 2048, results: []LLMResult{
		stopResult("Holds every module in the repository"),
		stopResult(enumeration),
		stopResult("Resolves configuration precedence"),
		stopResult("Facts flow up the tree"),
		stopResult("Failures abort the page"),
	}}
	p := newTestPipeline(t, client, &config.Config{
		DocsDir:               "docs",
		CharsPerToken:         config.CharsPerTokenDefault,
		SystemPrompt:          "system",
		ModuleSynthesisPrompt: "module",
	})

	page, err := buildModulePage(context.Background(), p, "cmd", []moduleComponent{{name: "File: client.go", facts: "facts"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(page, "### File: client.go\nTalks to the Ollama API and streams the result\n") {
		t.Errorf("expected exactly one line for the component, got:\n%s", page)
	}
	if strings.Contains(page, "Caches every response") || strings.Contains(page, "Retries a failed call") {
		t.Errorf("expected the trailing bullets to be dropped, got:\n%s", page)
	}
}

func TestBuildStandardDocPageFailsOnAnySection(t *testing.T) {
	cfg := &config.Config{SystemPrompt: "system", ArchitecturePrompt: "architecture", CharsPerToken: config.CharsPerTokenDefault}
	client := &scriptedLLM{numCtx: 2048, results: []LLMResult{
		stopResult("A generated wiki."),
		truncatedResult("half a boundar"),
	}}

	page, err := buildStandardDocPage(context.Background(), client, cfg, architecturePage, "# Module: .", func(t EventType, msg string) {})
	if !errors.Is(err, ErrTruncatedOutput) {
		t.Fatalf("expected ErrTruncatedOutput, got %v (page: %q)", err, page)
	}
	if client.calls != 2 {
		t.Fatalf("expected the page to stop at the failing section, got %d calls", client.calls)
	}
}

func TestSlotNumPredict(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		kind slotKind
		want int
	}{
		{name: "line default", cfg: config.Config{}, kind: slotLine, want: config.SlotNumPredictDefault},
		{name: "line override", cfg: config.Config{SlotNumPredict: 64}, kind: slotLine, want: 64},
		{name: "paragraph default", cfg: config.Config{}, kind: slotParagraph, want: config.ParagraphNumPredictDefault},
		{name: "paragraph override", cfg: config.Config{ParagraphNumPredict: 2048}, kind: slotParagraph, want: 2048},
		{name: "line bound ignores the paragraph field", cfg: config.Config{ParagraphNumPredict: 2048}, kind: slotLine, want: config.SlotNumPredictDefault},
		{name: "paragraph bound ignores the line field", cfg: config.Config{SlotNumPredict: 64}, kind: slotParagraph, want: config.ParagraphNumPredictDefault},
		{name: "global cap wins for a line slot", cfg: config.Config{SlotNumPredict: 256, NumPredict: 100}, kind: slotLine, want: 100},
		{name: "global cap wins for a paragraph slot", cfg: config.Config{ParagraphNumPredict: 2048, NumPredict: 100}, kind: slotParagraph, want: 100},
		{name: "global cap used when the slot is unset", cfg: config.Config{NumPredict: 100}, kind: slotLine, want: 100},
		{name: "a global cap above the slot bound never raises it", cfg: config.Config{NumPredict: 4096}, kind: slotParagraph, want: config.ParagraphNumPredictDefault},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := slotNumPredict(&tc.cfg, tc.kind); got != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, got)
			}
		})
	}
}

func TestSlotRunnerOutputReserveTakesTheLargestBound(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want int
	}{
		{name: "defaults", cfg: config.Config{}, want: config.ParagraphNumPredictDefault},
		{name: "paragraph bound raised", cfg: config.Config{ParagraphNumPredict: 4096}, want: 4096},
		{name: "line bound only", cfg: config.Config{SlotNumPredict: 64}, want: config.ParagraphNumPredictDefault},
		{name: "both bounds lowered", cfg: config.Config{SlotNumPredict: 64, ParagraphNumPredict: 128}, want: 128},
		{name: "global cap below both", cfg: config.Config{NumPredict: 128}, want: 128},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := newSlotRunner(context.Background(), &scriptedLLM{}, &tc.cfg, "style", func(EventType, string) {})
			if got := runner.outputReserve(); got != tc.want {
				t.Fatalf("expected %d, got %d", tc.want, got)
			}
		})
	}
}

func TestComponentFactsDigestIsBounded(t *testing.T) {
	components := []moduleComponent{
		{name: "File: client.go", facts: strings.Repeat("a", 500)},
		{name: "File: tree.go", facts: strings.Repeat("b", 500)},
	}

	digest := componentFactsDigest(components, 100)
	if utf8Len := len([]rune(digest)); utf8Len > 100+len(components)*64 {
		t.Fatalf("expected the digest to be bounded near the budget, got %d runes", utf8Len)
	}
	if !strings.Contains(digest, "### File: client.go") || !strings.Contains(digest, "### File: tree.go") {
		t.Errorf("expected every component in the digest, got:\n%s", digest)
	}
	if !strings.HasSuffix(digest, "...") {
		t.Errorf("expected a truncation marker on the last cut component, got:\n%s", digest)
	}
}

func TestChildIdentityFactsKeepsTheChildIdentity(t *testing.T) {
	child := renderModulePage("internal/config", modulePageParts{
		responsibility: "Resolves the configuration",
		components:     []moduleComponentLine{{name: "File: resolve.go", text: "Merges flags, env and yaml"}},
		dataFlow:       "The flags arrive first and the yaml last.",
		errorHandling:  "An invalid value fails the whole run.",
	})

	head := childIdentityFacts(child)
	if !strings.Contains(head, "## Responsibility\nResolves the configuration") {
		t.Errorf("expected the child responsibility in the head, got:\n%s", head)
	}
	if !strings.Contains(head, "### File: resolve.go\nMerges flags, env and yaml") {
		t.Errorf("expected the child components in the head, got:\n%s", head)
	}
	if strings.Contains(head, "The flags arrive first") || strings.Contains(head, "An invalid value fails") {
		t.Errorf("expected the child interior to be dropped, got:\n%s", head)
	}
	if got := childIdentityFacts("no skeleton here"); got != "no skeleton here" {
		t.Errorf("expected an unskeletonized page to be kept whole, got %q", got)
	}
}

func TestModuleShapeSelectsParentFraming(t *testing.T) {
	components := parseModuleComponents([]string{
		"### File: main.go\n#### [API_SIGNATURES]\nfunc main()",
		"### Subsystem: config\n# Module: internal/config\n\n## Responsibility\nResolves the configuration",
	})

	if got := moduleShape(components); got != shapeHierarchical {
		t.Fatalf("expected a module with a subsystem to be hierarchical, got %v", got)
	}
	dataFlow, errorHandling := moduleParagraphPrompts(".", componentIdentities(components), "digest", moduleShape(components))
	if !strings.Contains(dataFlow, "what flows between the components") {
		t.Errorf("expected the parent data flow framing, got:\n%s", dataFlow)
	}
	if !strings.Contains(errorHandling, "how errors cross the boundaries") {
		t.Errorf("expected the parent error framing, got:\n%s", errorHandling)
	}
	if !strings.Contains(errorHandling, "do not repeat it") {
		t.Errorf("expected the parent error prompt to forbid restating the children, got:\n%s", errorHandling)
	}

	leaf := parseModuleComponents([]string{"### File: main.go\nfunc main()"})
	if got := moduleShape(leaf); got != shapeLeaf {
		t.Fatalf("expected a module without a subsystem to be a leaf, got %v", got)
	}
	dataFlow, errorHandling = moduleParagraphPrompts("cmd", componentIdentities(leaf), "digest", moduleShape(leaf))
	if !strings.Contains(dataFlow, "Describe how data moves through this module") {
		t.Errorf("expected the leaf data flow framing, got:\n%s", dataFlow)
	}
	if !strings.Contains(errorHandling, "Describe how this module reports and handles errors") {
		t.Errorf("expected the leaf error framing, got:\n%s", errorHandling)
	}
}

func TestBuildModulePageKeepsChildInteriorOutOfTheParentSlots(t *testing.T) {
	child := renderModulePage("internal/config", modulePageParts{
		responsibility: "Resolves the configuration",
		components:     []moduleComponentLine{{name: "File: resolve.go", text: "Merges flags, env and yaml"}},
		dataFlow:       "The flags arrive first and the yaml last.",
		errorHandling:  "An invalid value fails the whole run.",
	})
	components := parseModuleComponents([]string{
		"### File: root.go\n#### [API_SIGNATURES]\nfunc main()",
		markdownHeadingPrefix + subsystemPrefix + "config\n" + child,
	})
	client := &scriptedLLM{numCtx: 2048, results: []LLMResult{
		stopResult("Holds the command layer"),
		stopResult("Owns the entry point"),
		stopResult("Owns the configuration"),
		stopResult("The command layer hands the resolved config to the engine"),
		stopResult("Every error travels back out through the command layer"),
	}}
	p := newTestPipeline(t, client, &config.Config{
		DocsDir:               "docs",
		CharsPerToken:         config.CharsPerTokenDefault,
		SystemPrompt:          "system",
		ModuleSynthesisPrompt: "module",
	})

	if _, err := buildModulePage(context.Background(), p, ".", components); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i, prompt := range client.prompts {
		if strings.Contains(prompt, "The flags arrive first") || strings.Contains(prompt, "An invalid value fails") {
			t.Errorf("call %d: expected the child interior to stay out of the parent slots, got:\n%s", i, prompt)
		}
	}
	if !strings.Contains(client.prompts[2], "Resolves the configuration") {
		t.Errorf("expected the child identity in the subsystem component slot, got:\n%s", client.prompts[2])
	}
	if !strings.Contains(client.prompts[3], "what flows between the components") {
		t.Errorf("expected the parent framing in the data flow slot, got:\n%s", client.prompts[3])
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello", 10); got != "hello" {
		t.Errorf("expected no truncation, got %q", got)
	}
	if got := truncateRunes("hello", 0); got != "" {
		t.Errorf("expected an empty payload for a zero budget, got %q", got)
	}
	if got := truncateRunes("héllo wörld", 5); got != "héllo..." {
		t.Errorf("expected a rune-safe cut, got %q", got)
	}
}
