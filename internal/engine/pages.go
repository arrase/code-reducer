package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/arrase/code-reducer/internal/config"
)

// slotKind selects how a slot answer is normalized.
type slotKind int

const (
	// slotLine is a micro-task whose answer must collapse to one clean line.
	slotLine slotKind = iota
	// slotParagraph is a micro-task whose answer must collapse to one clean paragraph.
	slotParagraph
)

// docSlot is one bounded LLM micro-task. The page structure is owned by the code,
// so a slot carries only what a single call needs: the format contract, the minimal
// context to answer it, and the subject used for logging and error reporting.
type docSlot struct {
	kind    slotKind
	subject string
	prompt  string
	logMsg  string
}

// namedSection is a heading the code owns together with the prose that fills it.
type namedSection struct {
	heading string
	text    string
}

// slotRunner fills prose slots sequentially against a single LLM client. Every slot
// is an independent, hard-bounded call: no slot shares a generation budget with
// another, and a failure aborts the page instead of leaving a section out.
type slotRunner struct {
	ctx      context.Context
	client   llmCaller
	system   string
	style    string
	cfg      *config.Config
	logEvent LogEventFunc
}

// newSlotRunner builds a runner for prose styled by the given page style prompt.
// The style prompt opens the user turn rather than the system message, so the
// system message is cfg.SystemPrompt on every call of a run and the style prompt
// stays the shared prefix of every slot of this page kind.
func newSlotRunner(ctx context.Context, c llmCaller, cfg *config.Config, stylePrompt string, logEvent LogEventFunc) slotRunner {
	return slotRunner{
		ctx:      ctx,
		client:   c,
		system:   cfg.SystemPrompt,
		style:    stylePrompt,
		cfg:      cfg,
		logEvent: logEvent,
	}
}

// numPredict returns the generation bound of a slot kind. The bound is selected at
// call time because a page mixes line slots and paragraph slots, and the two need
// very different amounts of room.
func (r slotRunner) numPredict(kind slotKind) int {
	return slotNumPredict(r.cfg, kind)
}

// outputReserve returns the context tokens a prompt payload must leave for
// generation: the largest bound any slot of this page may ask for, so sizing a
// payload against a line bound could leave a paragraph slot no room to answer.
func (r slotRunner) outputReserve() int {
	bound := r.numPredict(slotLine)
	if paragraph := r.numPredict(slotParagraph); paragraph > bound {
		bound = paragraph
	}
	return bound
}

// slotNumPredict returns the generation bound of a prose slot of the given kind.
// A line slot is trimmed by the code to its first sentence and to a 40 word budget,
// so it only needs room for that first sentence, while a paragraph slot keeps
// every sentence the model wrote and needs the larger bound. A lower explicit
// client-wide cap wins, so a user who lowers num_predict never gets a larger budget
// back from a slot default.
func slotNumPredict(cfg *config.Config, kind slotKind) int {
	budget := cfg.ParagraphNumPredict
	if kind == slotLine {
		budget = cfg.SlotNumPredict
	}
	if budget <= 0 {
		budget = slotNumPredictDefault(kind)
	}
	if cfg.NumPredict > 0 && cfg.NumPredict < budget {
		return cfg.NumPredict
	}
	return budget
}

// slotNumPredictDefault returns the shipped generation bound of a slot kind.
func slotNumPredictDefault(kind slotKind) int {
	if kind == slotLine {
		return config.SlotNumPredictDefault
	}
	return config.ParagraphNumPredictDefault
}

// fill runs one slot micro-task and returns the normalized prose for it.
func (r slotRunner) fill(slot docSlot) (string, error) {
	if err := r.ctx.Err(); err != nil {
		return "", err
	}
	r.logEvent(EventStatus, slot.logMsg)

	res, err := r.client.CallLLMWithNumPredict(r.ctx, r.system, []Message{{Role: "user", Content: userMessage(r.style, slot.prompt)}}, r.numPredict(slot.kind))
	if err != nil {
		return "", fmt.Errorf("%s: %w", slot.subject, err)
	}

	var content string
	if salvaged := slot.kind.salvage(res); salvaged != "" {
		r.logEvent(EventStatus, fmt.Sprintf("  ↳ %s of an over-long answer: %s", slot.kind.salvagedSpan(), slot.subject))
		content = salvaged
	} else {
		content, err = requireCompleteContent(res, slot.subject)
		if err != nil {
			return "", err
		}
	}

	text := slot.kind.sanitize(content)
	if text == "" {
		return "", fmt.Errorf("%s: %w: the answer carried no prose once formatting was normalized", slot.subject, ErrEmptyOutput)
	}
	return text, nil
}

// salvage returns the usable part of a slot answer that stopped on the generation
// limit, and an empty string for every other case. A line slot keeps its first
// complete sentence and a paragraph slot keeps everything up to its last complete
// sentence, because both prefixes are well-formed instances of what the slot asked
// for: aborting a whole document because the model wrote too much is the wrong
// failure mode. The call was bounded, so the salvage costs no more tokens than the
// answer it replaces. An answer with no complete sentence yields nothing, which
// sends the slot to the hard failure requireCompleteContent reports.
func (k slotKind) salvage(res LLMResult) string {
	if res.DoneReason != doneReasonLength {
		return ""
	}
	content := stripOuterMarkdownFence(res.Content)
	if k == slotLine {
		return firstCompleteSentence(content)
	}
	return lastCompleteSentence(content)
}

// salvagedSpan names the prefix salvage kept, so the log line reads correctly for
// both slot kinds.
func (k slotKind) salvagedSpan() string {
	if k == slotLine {
		return "kept the first complete sentence"
	}
	return "kept the text up to the last complete sentence"
}

// sanitize normalizes a slot answer to the shape its contract requires.
func (k slotKind) sanitize(content string) string {
	if k == slotLine {
		return sanitizeSlotLine(content)
	}
	return sanitizeSlotParagraph(content)
}

// moduleComponent is one Go-owned entry of a module Components section: the identity
// the code assigns to a file or a child subsystem, plus the facts a slot call is
// allowed to read. A subsystem carries the rendered page of the child module.
type moduleComponent struct {
	name      string
	facts     string
	subsystem bool
}

// moduleComponentLine is a component identity together with its generated prose.
type moduleComponentLine struct {
	name string
	text string
}

// modulePageParts is the full set of prose slots of a module page.
type modulePageParts struct {
	responsibility string
	components     []moduleComponentLine
	dataFlow       string
	errorHandling  string
}

// parseModuleComponents turns the collectComponents output into ordered components.
// The identities are kept exactly as collectComponents wrote them, which is what
// makes the rendered headings deterministic.
func parseModuleComponents(components []string) []moduleComponent {
	parsed := make([]moduleComponent, 0, len(components))
	for _, raw := range components {
		heading, facts, _ := strings.Cut(raw, "\n")
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(heading), markdownHeadingPrefix))
		parsed = append(parsed, moduleComponent{
			name:      name,
			facts:     strings.TrimSpace(facts),
			subsystem: strings.HasPrefix(name, subsystemPrefix),
		})
	}
	return parsed
}

// moduleShape reports whether a node has child subsystems. A module with children is
// documented at the boundary between them, which is a different question from the
// one a leaf module answers about its own files.
func moduleShape(components []moduleComponent) moduleNodeShape {
	for _, c := range components {
		if c.subsystem {
			return shapeHierarchical
		}
	}
	return shapeLeaf
}

// moduleNodeShape selects the framing of the module page slots.
type moduleNodeShape int

const (
	// shapeLeaf is a module made of files only.
	shapeLeaf moduleNodeShape = iota
	// shapeHierarchical is a module that owns child subsystems.
	shapeHierarchical
)

// readComponents returns the components as the slots of this page may read them. A
// child subsystem is reduced to the head of its own page: its Data Flow and Error
// Handling paragraphs describe the child's interior, which the child page already
// documents, and handing them to the parent is what makes a parent restate its
// children. File facts are untouched, so a leaf module reads exactly as before.
func readComponents(components []moduleComponent) []moduleComponent {
	read := make([]moduleComponent, len(components))
	copy(read, components)
	for i := range read {
		if read[i].subsystem {
			read[i].facts = childIdentityFacts(read[i].facts)
		}
	}
	return read
}

// childIdentityFacts keeps the part of a child module page a parent needs to reason
// about the child: its title, its responsibility and its component entries.
func childIdentityFacts(page string) string {
	head, _, found := strings.Cut(page, "\n## "+headingDataFlow+"\n")
	if !found {
		return page
	}
	return strings.TrimSpace(head)
}

// buildModulePage fills every prose slot of a module page with one bounded
// micro-task per slot and renders the result from the fixed skeleton. The caller
// must supply at least one component, which synthesizeNode guarantees.
func buildModulePage(ctx context.Context, p *pipelineState, nodePath string, components []moduleComponent) (string, error) {
	runner := newSlotRunner(ctx, p.client, p.cfg, p.cfg.ModuleSynthesisPrompt, p.logEvent)
	promptBudget := promptCharBudget(p.client.NumCtx(), runner.outputReserve(), p.cfg.CharsPerToken)
	readable := readComponents(components)
	identities := componentIdentities(readable)
	digest := componentFactsDigest(readable, promptBudget)
	dataFlowPrompt, errorHandlingPrompt := moduleParagraphPrompts(nodePath, identities, digest, moduleShape(components))

	responsibility, err := runner.fill(docSlot{
		kind:    slotLine,
		subject: fmt.Sprintf("responsibility of module %s", nodePath),
		prompt: fmt.Sprintf("Describe the single responsibility of this module in one line.\n\nModule: %s\nComponents:\n%s",
			nodePath, identities),
		logMsg: fmt.Sprintf("➜ Describing responsibility of %s", nodePath),
	})
	if err != nil {
		return "", err
	}

	lines := make([]moduleComponentLine, 0, len(readable))
	for _, c := range readable {
		text, err := runner.fill(docSlot{
			kind:    slotLine,
			subject: fmt.Sprintf("component %s of module %s", c.name, nodePath),
			prompt: fmt.Sprintf("Describe this single component in one line.\n\nModule: %s\nComponent: %s\nFacts:\n%s",
				nodePath, c.name, truncateRunes(c.facts, promptBudget)),
			logMsg: fmt.Sprintf("➜ Describing component %s of %s", c.name, nodePath),
		})
		if err != nil {
			return "", err
		}
		lines = append(lines, moduleComponentLine{name: c.name, text: text})
	}

	dataFlow, err := runner.fill(docSlot{
		kind:    slotParagraph,
		subject: fmt.Sprintf("data flow of module %s", nodePath),
		prompt:  dataFlowPrompt,
		logMsg:  fmt.Sprintf("➜ Describing data flow of %s", nodePath),
	})
	if err != nil {
		return "", err
	}

	errorHandling, err := runner.fill(docSlot{
		kind:    slotParagraph,
		subject: fmt.Sprintf("error handling of module %s", nodePath),
		prompt:  errorHandlingPrompt,
		logMsg:  fmt.Sprintf("➜ Describing error handling of %s", nodePath),
	})
	if err != nil {
		return "", err
	}

	return renderModulePage(nodePath, modulePageParts{
		responsibility: responsibility,
		components:     lines,
		dataFlow:       dataFlow,
		errorHandling:  errorHandling,
	}), nil
}

// moduleParagraphPrompts returns the Data Flow and Error Handling instructions of a
// module page. A module that owns subsystems is documented across the boundary
// between them: what flows from one subsystem to the next, and how errors cross it.
// A leaf module has no such boundary and keeps the per-component framing. Both
// variants see the same identities and the same digest, so the choice is a framing
// decision and not a change of evidence.
func moduleParagraphPrompts(nodePath, identities, digest string, shape moduleNodeShape) (string, string) {
	if shape == shapeHierarchical {
		return fmt.Sprintf("Describe what flows between the components of this module and what each one hands to the next, in a short paragraph. Each component has its own page, so never restate what a component does internally.\n\nModule: %s\nComponents:\n%s\nFacts:\n%s",
				nodePath, identities, digest),
			fmt.Sprintf("Describe how errors cross the boundaries between the components of this module and how they reach the caller, in a short paragraph. Each component page covers its own error handling, so do not repeat it.\n\nModule: %s\nComponents:\n%s\nFacts:\n%s",
				nodePath, identities, digest)
	}
	return fmt.Sprintf("Describe how data moves through this module in a short paragraph.\n\nModule: %s\nComponents:\n%s\nFacts:\n%s",
			nodePath, identities, digest),
		fmt.Sprintf("Describe how this module reports and handles errors in a short paragraph.\n\nModule: %s\nComponents:\n%s\nFacts:\n%s",
			nodePath, identities, digest)
}

// renderModulePage assembles a module page from the fixed skeleton. The title, the
// section headings and their order, and the component names and their order are all
// decided here and never by the model.
func renderModulePage(nodePath string, parts modulePageParts) string {
	sections := []namedSection{
		{heading: headingResponsibility, text: parts.responsibility},
		{heading: headingComponents, text: renderComponentBlock(parts.components)},
		{heading: headingDataFlow, text: parts.dataFlow},
		{heading: headingErrorHandling, text: parts.errorHandling},
	}
	return renderDocPage("Module: "+nodePath, sections)
}

// renderComponentBlock renders the nested component entries of a module page. The
// leading newline separates the block from its section heading.
func renderComponentBlock(components []moduleComponentLine) string {
	if len(components) == 0 {
		return ""
	}
	entries := make([]string, 0, len(components))
	for _, c := range components {
		entries = append(entries, fmt.Sprintf("%s%s\n%s", markdownHeadingPrefix, c.name, c.text))
	}
	return "\n" + strings.Join(entries, "\n\n")
}

// docSection is one prose slot of a standard documentation page.
type docSection struct {
	heading     string
	instruction string
}

// standardDocPage describes a standard documentation page: the title that heads
// it, the file it is written to, the sections it owns, and the label of the
// generated content its sections are derived from.
type standardDocPage struct {
	title       string
	fileName    string
	sourceLabel string
	sections    []docSection
}

// architecturePage and quickstartPage define the fixed section order of the two
// standard pages. Their headings come from here, never from the model, and the
// quickstart is derived from the architecture page rather than from the raw root
// module summary the architecture page was built from.
var architecturePage = standardDocPage{
	title:       "Architecture",
	fileName:    "architecture.md",
	sourceLabel: "Root module page:",
	sections: []docSection{
		{heading: "Overview", instruction: "Describe the purpose of this project in a short paragraph."},
		{heading: "System Boundaries", instruction: "Describe what this project owns and what it leaves to external systems in a short paragraph."},
		{heading: "Module Interaction", instruction: "Describe how the modules of this project interact in a short paragraph."},
	},
}

var quickstartPage = standardDocPage{
	title:       "Quickstart",
	fileName:    "quickstart.md",
	sourceLabel: "Architecture page:",
	sections: []docSection{
		{heading: "What This Project Does", instruction: "Describe what this project does for its users in a short paragraph."},
		{heading: "Project Layout", instruction: "Describe the repository layout and where a new developer should start reading, in a short paragraph."},
		{heading: "Common Workflows", instruction: "Describe the common developer workflows in a short paragraph."},
	},
}

// buildStandardDocPage fills every section of a standard page with one bounded
// micro-task per section and renders the page from the fixed skeleton. The source
// is the generated content the page is derived from, bounded to the prompt budget.
func buildStandardDocPage(ctx context.Context, c llmCaller, cfg *config.Config, page standardDocPage, source string, logEvent LogEventFunc) (string, error) {
	runner := newSlotRunner(ctx, c, cfg, cfg.ArchitecturePrompt, logEvent)
	budget := promptCharBudget(c.NumCtx(), runner.outputReserve(), cfg.CharsPerToken)
	summaries := truncateRunes(source, budget)

	parts := make([]namedSection, 0, len(page.sections))
	for _, s := range page.sections {
		text, err := runner.fill(docSlot{
			kind:    slotParagraph,
			subject: fmt.Sprintf("%s section of the %s page", s.heading, page.title),
			prompt:  fmt.Sprintf("%s\n\n%s\n%s", s.instruction, page.sourceLabel, summaries),
			logMsg:  fmt.Sprintf("➜ Writing the %s section of the %s page", s.heading, page.title),
		})
		if err != nil {
			return "", err
		}
		parts = append(parts, namedSection{heading: s.heading, text: text})
	}
	return renderDocPage(page.title, parts), nil
}

// renderDocPage assembles a documentation page from a fixed skeleton: the code owns
// the title, the headings and their order, the model only fills the prose.
func renderDocPage(title string, sections []namedSection) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n", title)
	for _, s := range sections {
		fmt.Fprintf(&b, "\n## %s\n%s\n", s.heading, s.text)
	}
	return b.String()
}

// componentIdentities renders the component names of a module, without their facts.
func componentIdentities(components []moduleComponent) string {
	names := make([]string, 0, len(components))
	for _, c := range components {
		names = append(names, "- "+c.name)
	}
	return strings.Join(names, "\n")
}

// componentFactsDigest renders the component facts of a module into a payload of at
// most maxChars runes, splitting the budget evenly across components so a whole
// module slot never re-sends an entire subtree.
func componentFactsDigest(components []moduleComponent, maxChars int) string {
	if len(components) == 0 {
		return ""
	}
	share := maxChars / len(components)
	digest := make([]string, 0, len(components))
	for _, c := range components {
		digest = append(digest, fmt.Sprintf("### %s\n%s", c.name, truncateRunes(c.facts, share)))
	}
	return strings.Join(digest, "\n\n")
}

// truncateRunes bounds a prompt payload to maxRunes runes. The cut is marked so the
// model never reads a truncated payload as the complete picture.
func truncateRunes(content string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(content)
	if len(runes) <= maxRunes {
		return content
	}
	return strings.TrimSpace(string(runes[:maxRunes])) + "..."
}
