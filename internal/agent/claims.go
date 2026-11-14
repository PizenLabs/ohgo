package agent

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Claim scope is the answer-side complement to evidence sufficiency.
//
// The evidence ledger can prove that a run observed a manifest, a Compose file,
// or a source file. Those observations are not interchangeable: a dependency
// manifest declares a library, Compose declares a topology, and only a source
// observation or a successful execution establishes what the implementation
// does. This file keeps an informational answer from promoting one class into a
// stronger one.
//
// The checks are deliberately narrow and deterministic. They are completion
// vetoes, never authority, and they do not judge whether a supported claim is
// true. A false positive would turn an honest answer into NEEDS_REVIEW, so each
// pattern requires a concrete behavior, metric, deployment, or path-scoped
// speculation rather than a generic assertion.
type claimIssue struct {
	// Code is the grounding issue kind.
	Code string
	// Detail explains the missing observation and the narrower claim.
	Detail string
}

var (
	// claimSentenceBoundary splits an answer into claim-sized clauses. It is
	// finer than the completion sentence splitter because a semicolon or a
	// contrast can join a supported claim and an unsupported one; auditing
	// them as one sentence would let the supported half hide the other.
	claimSentenceBoundary = regexp.MustCompile(`[.!?]+[ \t]+|\n+|;\s+|,\s+but\s+`)

	// behaviorClaimPattern matches a declarative claim about what a service
	// does at runtime. Dependency names, Compose topology, and documentation
	// prose do not satisfy it.
	behaviorClaimPattern = regexp.MustCompile(`(?i)\b(?:` +
		`validat(?:e|es|ed|ing)\s+(?:against|with)\b|` +
		`publish(?:es|ing)?\b|` +
		`consum(?:e|es|ing)?\s+(?:the\s+)?(?:[a-z0-9_-]+\s+){0,2}(?:cart|queue|message|event|stream|api|service|order|product|payment)s?\b|` +
		`subscrib(?:e|es|ing)?\s+to\b|` +
		`fulfil(?:l(?:s|ing)?)?\b|` +
		`call(?:s|ing)?\s+(?:the\s+)?[a-z0-9_-]+\s+(?:service|api|endpoint)\b|` +
		`send(?:s|ing)?\s+(?:an?\s+)?(?:request|event|message)\s+to\b|` +
		`read(?:s|ing)?\s+from\b|` +
		`writ(?:e|es|ing)?\s+to\b|` +
		`auto[- ]?appl(?:y|ies|ied|ying)\b|` +
		`execut(?:e|es|ing)?\s+(?:the\s+)?(?:job|task|command|process|query|migration|workflow|pipeline|handler|request)\b|` +
		`(?:is|are)\s+running\s+(?:the\s+)?(?:job|task|command|process|query|migration|workflow|pipeline|handler|request)\b|` +
		`rout(?:e|es|ing)?\s+(?:requests?\s+)?(?:to|through)\b|` +
		`listen(?:s|ing)?\s+(?:on|for)\b|` +
		`process(?:es|ing)?\s+(?:orders?|payments?|events?)\b|` +
		`handl(?:e|es|ing)?\s+(?:orders?|requests?|events?)\b|` +
		`stor(?:e|es|ing)?\s+(?:data|orders?|sessions?)\s+in\b|` +
		`connect(?:s|ing)?\s+to\b` +
		`)`)

	// runtimeStateClaimPattern matches a claim that a project runtime is
	// currently alive. Configuration declares that a service exists; source
	// content describes what it would do; only a live runtime observation
	// establishes that it is running now.
	runtimeStateClaimPattern = regexp.MustCompile(`(?i)\b(?:` +
		`service|server|application|app|process|container|instance|worker|` +
		`gateway|backend|frontend|endpoint|project` +
		`)\b[^.\n]{0,40}\b(?:is|are|was|were)\s+` +
		`(?:currently\s+|now\s+|still\s+)?` +
		`(?:running|live|up|healthy|serving|ready)\b`)

	// testCountClaimPattern matches a concrete test-inventory count. A test
	// count is an inventory observation, not a documentation fact. The two
	// alternatives cover "the project has 120 tests" and "120 tests are
	// present"; a bare count in prose is left to provenance.
	testCountClaimPattern = regexp.MustCompile(`(?i)\b(?:` +
		`(?:there\s+(?:are|is)|contains?|has|have|reports?|totals?|total\s+of|with)\s+` +
		`(?:about\s+|approximately\s+|around\s+)?(\d[\d,]*)\s+` +
		`(?:[a-z][a-z0-9-]*\s+){0,2}(?:tests?|test\s+files?|test\s+cases?|test\s+functions?)\b|` +
		`(\d[\d,]*)\s+(?:[a-z][a-z0-9-]*\s+){0,2}` +
		`(?:tests?|test\s+files?|test\s+cases?|test\s+functions?)\s+(?:are|is)\s+present\b` +
		`)`)

	// structuralRelationClaimPattern matches an explicit code-level relation
	// between two endpoints. It requires a dotted endpoint, so a component
	// behavior sentence ("cart calls product") stays with the behavior audit.
	structuralRelationClaimPattern = regexp.MustCompile(`(?i)\b(` +
		`[a-z_][a-z0-9_]*(?:\.[a-z_][a-z0-9_]*)*` +
		`)\s+(calls?|invokes?|references?|depends\s+on)\s+(` +
		`[a-z_][a-z0-9_]*(?:\.[a-z_][a-z0-9_]*)*` +
		`)\b`)

	// deploymentClaimPattern matches a conclusion that services can be
	// deployed independently. Separate service declarations are not that
	// conclusion.
	deploymentClaimPattern = regexp.MustCompile(`(?i)\b(?:` +
		`(?:independently|separately)[\s-]+(?:deploy(?:ed|able|ment)?|scaled)\b|` +
		`deploy(?:ed|able)?\s+independently\b|` +
		`separate(?:ly)?\s+(?:deploy(?:ed|ment)?|release)s?\b` +
		`)`)

	// lineCountClaimPattern matches a concrete line-count or LOC claim. A
	// byte size printed by a directory listing is not a line count.
	lineCountClaimPattern = regexp.MustCompile(
		`(?i)\b(?:~|about\s+|approx(?:imately)?\s+|around\s+|roughly\s+)?([0-9][0-9,]*)\s*(?:LOC\b|lines?\s+of\s+code\b|lines?\b)`)

	// claimPathPattern extracts a repository coordinate from the same
	// sentence. It includes documentation and manifest suffixes because a
	// line-count claim about README.md is valid when that file was read.
	claimPathPattern = regexp.MustCompile(
		`(?i)(?:[a-z0-9_.-]+/)*[a-z0-9_.-]+\.(?:go|tsx?|jsx?|mjs|cjs|py|rs|java|kt|kts|rb|php|cs|md|markdown|rst|adoc|txt|json|ya?ml|toml|ini|conf|sql|sh|bash|proto|graphql|vue|svelte|c|cc|cpp|h|hpp|swift|scala)\b`)

	// speculationPattern marks an inference about content the run has not
	// read. The check only applies when the same sentence names the path, so
	// a general "the backend likely uses PostgreSQL" is not forced to repeat
	// the manifest observation.
	speculationPattern = regexp.MustCompile(
		`(?i)\b(?:likely|probably|presumably|possibly|seems?\s+to|appears?\s+to|suggests?\b|may\s+(?:be|contain|include|have)|might\s+(?:be|contain|include|have))\b`)

	// claimDirectoryPattern extracts a directory coordinate. It is a fallback
	// for a sentence that names a directory rather than a file.
	claimDirectoryPattern = regexp.MustCompile(`(?i)(?:[a-z0-9_.-]+/)+`)

	// speculativeContentPattern names content categories that can be checked
	// against the observed text for the referenced path.
	speculativeContentPattern = regexp.MustCompile(
		`(?i)\b(schemas?|edge\s+functions?|routes?|pages?|handlers?|controllers?|services?|tests?|ci|migrations?|data\s+layer|apis?|databases?|queues?|brokers?|caches?|auth(?:entication)?|sessions?|models?|endpoints?|workers?)\b`)

	// boundedObservationPattern reports a claim about what the probes found
	// in this pass, not a claim that the property is absent from the
	// repository.
	boundedObservationPattern = regexp.MustCompile(
		`(?i)\b(?:surfaced|found|detected|observed|seen|encountered|reported)\b[^.\n]{0,80}\b(?:in|during|from|by)\s+(?:the\s+|this\s+)?(?:probes?|pass|scan|search|listing|observations?|run|investigation)\b`)

	// boundedRunPattern reports that a check did not run. That is a fact
	// about the run, not a repository absence.
	boundedRunPattern = regexp.MustCompile(
		`(?i)\bno\s+(?:tests?|checks?|commands?)\s+(?:were|was|have\s+been|has\s+been)?\s*(?:run|executed|performed)\b`)

	// boundedScopePattern reports a claim limited to what this run actually
	// inspected: "no tests detected in inspected scope", "no API routes in the
	// files read". The limitation is part of the claim, not boilerplate around
	// a repository-wide conclusion.
	boundedScopePattern = regexp.MustCompile(
		`(?i)\b(?:in|within)\s+(?:the\s+|this\s+|our\s+|an?\s+)?(?:inspected|read|queried|listed|observed|examined|reviewed|sampled)\s+(?:scope|source|files?|code|content|portion|part|subset|range|listing|inventory)\b`)

	// boundedUnqueriedPattern reports that the check behind a negative claim
	// never ran, which is a run fact rather than a repository property. It is
	// limited to inspection verbs: "tests are not run" describes the project,
	// and boundedRunPattern already covers the run-fact form ("no tests were
	// run").
	boundedUnqueriedPattern = regexp.MustCompile(
		`(?i)\b(?:not|never)\s+(?:been\s+)?(?:queried|evaluated|inspected|checked|probed|searched|observed|read|examined)\b`)

	// globalNegativeScopePattern prevents a bounded-search phrase from
	// excusing the same sentence's repository-wide conclusion.
	globalNegativeScopePattern = regexp.MustCompile(
		`(?i)\b(?:in|across|throughout)\s+(?:the\s+)?(?:repository|repo|project|codebase|workspace|directory|folder)\b|\banywhere\b|\bat\s+all\b`)

	// claimedPortPattern extracts a concrete port literal from a behavior
	// sentence. It requires a colon or the word "port" so a version, a count,
	// or a timestamp is not mistaken for a service coordinate.
	claimedPortPattern = regexp.MustCompile(`(?i)(?::|\bport\s+)(\d{2,5})\b`)
)

// claimScopeIssues returns the answer claims the run's observations cannot
// support. It is called only for an informational objective; a mutation answer
// is governed by target and verification evidence instead.
func claimScopeIssues(answer string, turns []Turn) []claimIssue {
	answer = stripFencedCode(answer)
	var issues []claimIssue
	for _, raw := range claimSentenceBoundary.Split(strings.TrimSpace(answer), -1) {
		sentence := strings.TrimSpace(raw)
		if sentence == "" || claimIsNonAssertive(sentence) || claimIsBounded(sentence) ||
			claimIsDocumentationAttributed(sentence) {
			continue
		}
		lineIssue := lineCountClaimIssue(sentence, turns)
		if lineIssue != nil {
			issues = append(issues, *lineIssue)
		}
		if issue := implementationClaimIssue(sentence, turns); issue != nil {
			issues = append(issues, *issue)
		}
		if issue := conceptAbsenceIssue(sentence); issue != nil {
			issues = append(issues, *issue)
		}
		issues = append(issues, dependencyClaimIssues(sentence, turns)...)
		if issue := behaviorClaimIssue(sentence, turns); issue != nil {
			issues = append(issues, *issue)
		}
		if issue := runtimeStateClaimIssue(sentence, turns); issue != nil {
			issues = append(issues, *issue)
		}
		if issue := testInventoryClaimIssue(sentence, turns); issue != nil {
			issues = append(issues, *issue)
		}
		if issue := structuralRelationClaimIssue(sentence, turns); issue != nil {
			issues = append(issues, *issue)
		}
		if issue := citationClaimIssue(sentence, turns); issue != nil {
			issues = append(issues, *issue)
		}
		if issue := wiringClaimIssue(sentence, turns); issue != nil {
			issues = append(issues, *issue)
		}
		if issue := deploymentClaimIssue(sentence, turns); issue != nil {
			issues = append(issues, *issue)
		}
		// A line-count issue already explains why a metric about this path
		// is unsupported. Repeating the same path as speculation would only
		// crowd the bounded review reason.
		if lineIssue == nil {
			if issue := speculationClaimIssue(sentence, turns); issue != nil {
				issues = append(issues, *issue)
			}
		}
	}
	return prioritizeClaimIssues(dedupeClaimIssues(issues))
}

// prioritizeClaimIssues orders the review reason by consequence: an unsupported
// behavior claim is more misleading than a speculative detail, and a concrete
// line-count contradiction is more actionable than another inference. The
// caller still bounds the final reason; this ordering makes the first class of
// failure visible instead of letting answer order decide.
func prioritizeClaimIssues(issues []claimIssue) []claimIssue {
	rank := map[string]int{
		"unsupported_implementation_claim": 0,
		"unsupported_completeness_claim":   1,
		"unsupported_runtime_state":        2,
		"unsupported_dependency_claim":     3,
		"unsupported_wiring_claim":         4,
		"unsupported_test_inventory_claim": 4,
		"unsupported_structural_claim":     5,
		"unsupported_source_citation":      5,
		"unsupported_concept_absence":      6,
		"unsupported_deployment_claim":     7,
		"unsupported_line_count":           8,
		"unverified_speculation":           9,
	}
	sort.SliceStable(issues, func(i, j int) bool {
		return rank[issues[i].Code] < rank[issues[j].Code]
	})
	return issues
}

// lineCountClaimIssue checks a concrete line count against the file the run
// actually read. A directory listing prints a byte size; it is not a line
// count, and it must not be promoted into one.
func lineCountClaimIssue(sentence string, turns []Turn) *claimIssue {
	matches := lineCountClaimPattern.FindAllStringSubmatchIndex(sentence, -1)
	for _, match := range matches {
		if len(match) < 4 || match[2] < 0 || match[3] < 0 {
			continue
		}
		claimed := claimCount(sentence[match[2]:match[3]])
		if claimed <= 0 {
			continue
		}
		path := nearestClaimPath(sentence, match[0], match[1])
		if path == "" {
			if commandLineCountObserved(turns, claimed) {
				continue
			}
			return &claimIssue{
				Code: "unsupported_line_count",
				Detail: fmt.Sprintf(
					"the answer gives a line count of about %d but the run never measured one; read the file or run a line-count command, or remove the count",
					claimed),
			}
		}
		total, observed := observedReadLineCount(turns, path)
		if !observed {
			return &claimIssue{
				Code: "unsupported_line_count",
				Detail: fmt.Sprintf(
					"the answer gives %s about %d lines, but the run never read it; a listing reports bytes, not lines. Read it or remove the count",
					path, claimed),
			}
		}
		if !lineCountMatches(total, claimed) {
			return &claimIssue{
				Code: "unsupported_line_count",
				Detail: fmt.Sprintf(
					"the answer says %s has about %d lines, but the observed read reported %d; correct the count or cite the measurement",
					path, claimed, total),
			}
		}
	}
	return nil
}

// behaviorClaimIssue checks a runtime-behavior claim against source and live
// runtime observations. Configuration and manifests declare intent; they do
// not prove execution. A named port narrows the check to the observation that
// actually carries that port.
func behaviorClaimIssue(sentence string, turns []Turn) *claimIssue {
	matchIndex := behaviorClaimPattern.FindStringIndex(sentence)
	if matchIndex == nil {
		return nil
	}
	match := sentence[matchIndex[0]:matchIndex[1]]
	if autoApplyClaim(match) {
		// A database init hook is a configuration mechanism: the Compose
		// mount into docker-entrypoint-initdb.d proves that init scripts are
		// provided by configuration, even without a source read. Prose that
		// mentions the hook is not the mechanism.
		if observedMechanismContains(turns, "docker-entrypoint-initdb", "initdb", "entrypoint-initdb") {
			return nil
		}
		return &claimIssue{
			Code: "unsupported_implementation_claim",
			Detail: fmt.Sprintf(
				"the answer says %q, but no observed configuration or source shows that mechanism; verify it or mark it unverified",
				boundedClaim(match)),
		}
	}
	stem := behaviorStem(match)
	// Ports after the behavior verb belong to this claim. Ports earlier in the
	// sentence may describe the container mapping or another clause, so they
	// must not be attributed to the observed listener.
	ports := claimedPorts(sentence[matchIndex[1]:])
	// The component before the behavior verb is the subject. A claim about
	// cart is not supported by reading product, even when both contain the
	// same behavior word.
	subject := ComponentSubject(sentence, matchIndex[0])
	if stem == "" || behaviorClaimObserved(subject, stem, ports, turns) {
		return nil
	}
	detail := "the answer states %q as runtime behavior without a source observation; configuration declares topology, not execution. Verify it or mark it unverified"
	if len(ports) > 0 {
		detail = "the answer states %q at port %s without a source observation naming that port; configuration declares topology, not execution. Verify it or mark it unverified"
		return &claimIssue{
			Code:   "unsupported_implementation_claim",
			Detail: fmt.Sprintf(detail, boundedClaim(match), strings.Join(ports, ", ")),
		}
	}
	return &claimIssue{
		Code:   "unsupported_implementation_claim",
		Detail: fmt.Sprintf(detail, boundedClaim(match)),
	}
}

// claimedPorts returns the concrete port literals a behavior sentence names.
func claimedPorts(sentence string) []string {
	seen := map[string]bool{}
	var out []string
	for _, match := range claimedPortPattern.FindAllStringSubmatch(sentence, -1) {
		if len(match) < 2 || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		out = append(out, match[1])
	}
	return out
}

// behaviorClaimObserved reports whether source observations contain the
// behavior stem for the claimed component. When the sentence names ports,
// every claimed port must be present in a source observation that also carries
// the behavior stem: reading product's listener does not prove that cart
// listens on a different port, and a claim spanning several services may be
// supported by several source reads.
func behaviorClaimObserved(subject, stem string, ports []string, turns []Turn) bool {
	if len(ports) == 0 {
		return sourceObservationContains(turns, subject, stem, "") ||
			runtimeBehaviorObserved(turns, subject, stem)
	}
	for _, port := range ports {
		if sourceObservationContains(turns, subject, stem, port) || runtimePortObserved(turns, subject, port) {
			continue
		}
		return false
	}
	return true
}

// runtimeBehaviorObserved reports a live runtime whose own output carries the
// behavior stem. A runtime observation is behavior evidence, not a listener
// claim: it supports "connects" only when the observed process output says so.
func runtimeBehaviorObserved(turns []Turn, subject, stem string) bool {
	if stem == "" {
		return false
	}
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || outcome.Result.Runtime == nil {
				continue
			}
			runtime := outcome.Result.Runtime
			if subject != "" &&
				!ComponentMatchesText(subject, runtime.Command) &&
				!ComponentMatchesText(subject, runtime.URL) {
				continue
			}
			// The process output carries the behavior. The command and URL
			// name the component coordinate; a component called
			// "connect-service" must not become a "connects" observation.
			if strings.Contains(strings.ToLower(outcome.Result.Text), stem) {
				return true
			}
		}
	}
	return false
}

// runtimePortObserved reports a live runtime whose observed address names the
// port. A live service is stronger evidence than a source read for the port it
// actually answered on, but it does not prove unrelated behavior. When the
// claim names a component, the runtime's own command or URL must name that
// component; an observation of another service's listener is not reusable.
func runtimePortObserved(turns []Turn, subject, port string) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || outcome.Result.Runtime == nil {
				continue
			}
			runtime := outcome.Result.Runtime
			if !strings.Contains(runtime.URL, ":"+port) {
				continue
			}
			if subject != "" &&
				!ComponentMatchesText(subject, runtime.URL) &&
				!ComponentMatchesText(subject, runtime.Command) {
				continue
			}
			return true
		}
	}
	return false
}

// runtimeStateClaimIssue checks a claim that the project runtime is alive.
// A manifest, Compose entry, or source read describes what exists or what the
// code would do; none establishes that a process is currently running.
func runtimeStateClaimIssue(sentence string, turns []Turn) *claimIssue {
	match := runtimeStateClaimPattern.FindStringIndex(sentence)
	if match == nil || runtimeStateIdiom(sentence) || runtimeStateTransitive(sentence, match[1]) {
		return nil
	}
	subject := runtimeStateSubject(sentence, match)
	if runtimeStateObserved(turns, subject) {
		return nil
	}
	where := "the project"
	if subject != "" {
		where = subject
	}
	return &claimIssue{
		Code: "unsupported_runtime_state",
		Detail: fmt.Sprintf(
			"the answer states %q as current runtime state for %s, but the run observed no live runtime; configuration and source content declare what would run, not that it is running. Verify it or mark it unverified",
			boundedClaim(sentence[match[0]:match[1]]), where),
	}
}

// runtimeStateIdiom reports a phrase that uses "up" or "ready" without
// describing a running process.
func runtimeStateIdiom(sentence string) bool {
	lower := strings.ToLower(sentence)
	for _, marker := range []string{"up to date", "up-to-date", "up to you", "ready to", "ready for"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// runtimeStateTransitive reports "is running <object>" rather than "is
// running". The behavior audit owns the transitive form; the runtime-state
// audit owns the live-process form.
func runtimeStateTransitive(sentence string, end int) bool {
	if end < 0 || end >= len(sentence) {
		return false
	}
	rest := strings.ToLower(strings.TrimSpace(sentence[end:]))
	for _, prefix := range []string{
		"the ", "a ", "an ", "its ", "their ", "our ", "my ", "this ", "that ",
	} {
		if strings.HasPrefix(rest, prefix) {
			return true
		}
	}
	return false
}

// runtimeStateSubject reads the component before the copula. The matched span
// starts at the subject noun ("service is running"), so the prefix alone would
// omit a preceding qualifier ("cart service"); the copula is the subject
// boundary.
func runtimeStateSubject(sentence string, match []int) string {
	if len(match) < 2 {
		return ""
	}
	window := sentence[match[0]:match[1]]
	lower := strings.ToLower(window)
	for _, copula := range []string{" is ", " are ", " was ", " were "} {
		if index := strings.Index(lower, copula); index >= 0 {
			return ComponentSubject(sentence, match[0]+index)
		}
	}
	return ComponentSubject(sentence, match[0])
}

// runtimeStateObserved reports a live runtime observation bound to the subject.
// A bare address is not a component coordinate; the runtime's own command or
// URL must name the subject when the claim names one.
func runtimeStateObserved(turns []Turn, subject string) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || outcome.Result.Runtime == nil {
				continue
			}
			if subject == "" {
				return true
			}
			runtime := outcome.Result.Runtime
			if ComponentMatchesText(subject, runtime.Command) ||
				ComponentMatchesText(subject, runtime.URL) {
				return true
			}
		}
	}
	return false
}

// testInventoryClaimIssue checks a concrete test count against a complete,
// current, unscoped test inventory. A README count and a metadata overview are
// not a filtered inventory.
func testInventoryClaimIssue(sentence string, turns []Turn) *claimIssue {
	match := testCountClaimPattern.FindStringSubmatchIndex(sentence)
	if match == nil {
		return nil
	}
	raw := ""
	for index := 2; index+1 < len(match); index += 2 {
		if match[index] >= 0 && match[index+1] > match[index] {
			raw = sentence[match[index]:match[index+1]]
			break
		}
	}
	claimed, ok := claimCountAllowZero(raw)
	if !ok {
		return nil
	}
	counts, current := observedTestCounts(turns)
	if !current {
		return &claimIssue{
			Code: "unsupported_test_inventory_claim",
			Detail: fmt.Sprintf(
				"the answer states a test count of %d, but the run has no complete current unscoped test inventory; a metadata overview, README, or partial query cannot establish the count. Query structure view=tests or remove the count",
				claimed),
		}
	}
	if len(counts) == 0 {
		return &claimIssue{
			Code: "unsupported_test_inventory_claim",
			Detail: fmt.Sprintf(
				"the answer states a test count of %d, but the observed test inventory did not report a usable count; remove the count or cite the inventory",
				claimed),
		}
	}
	if counts[claimed] {
		return nil
	}
	return &claimIssue{
		Code: "unsupported_test_inventory_claim",
		Detail: fmt.Sprintf(
			"the answer states a test count of %d, but the current complete test inventory reported %s; correct the count or cite the observed inventory",
			claimed, joinTestCounts(counts)),
	}
}

// observedTestCounts returns the counts a complete current unscoped test
// inventory reported, or current=false when no such inventory exists.
func observedTestCounts(turns []Turn) (map[int]bool, bool) {
	counts := map[int]bool{}
	current := false
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || outcome.ToolName != "structure" {
				continue
			}
			if proposalArgString(outcome.Args, "path") != "" ||
				proposalArgString(outcome.Args, "package") != "" ||
				proposalArgString(outcome.Args, "symbol") != "" {
				continue
			}
			details, ok := outcome.Result.Details.(map[string]any)
			if !ok || detailValueString(details, "view") != "tests" {
				continue
			}
			if detailValueString(details, "graph_state") != "CURRENT" ||
				detailValueBool(details, "truncated") ||
				!detailValueBool(details, "scope_complete") ||
				!detailValueBool(details, "inventory_complete") ||
				!detailValueBool(details, "structural_complete") ||
				!nestedProjectsResolved(details) {
				continue
			}
			current = true
			for _, key := range []string{"test_files", "total"} {
				if value, known := details[key].(int); known {
					counts[value] = true
				}
			}
		}
	}
	return counts, current
}

// nestedProjectsResolved reports that every disclosed nested repository was
// included in the same snapshot. An unresolved boundary leaves the inventory
// unable to establish a repository-wide count or absence.
func nestedProjectsResolved(details map[string]any) bool {
	for _, project := range claimFileRows(details["nested_projects"]) {
		if included, ok := project["included"].(bool); !ok || !included {
			return false
		}
	}
	return true
}

// joinTestCounts renders observed counts deterministically.
func joinTestCounts(counts map[int]bool) string {
	values := make([]int, 0, len(counts))
	for value := range counts {
		values = append(values, value)
	}
	sort.Ints(values)
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, " or ")
}

// structuralRelationClaimIssue checks an explicit code-level relationship
// between two endpoints. A relationship is not implied by reading one of its
// endpoints; the observation must bind both of them.
func structuralRelationClaimIssue(sentence string, turns []Turn) *claimIssue {
	for _, match := range structuralRelationClaimPattern.FindAllStringSubmatchIndex(sentence, -1) {
		if len(match) < 8 {
			continue
		}
		left := sentence[match[2]:match[3]]
		verb := strings.TrimSpace(sentence[match[4]:match[5]])
		right := sentence[match[6]:match[7]]
		if !strings.Contains(left, ".") && !strings.Contains(right, ".") {
			continue
		}
		if structuralEndpointIsGeneric(left) || structuralEndpointIsGeneric(right) {
			continue
		}
		if structuralRelationObserved(turns, left, verb, right) {
			continue
		}
		return &claimIssue{
			Code: "unsupported_structural_claim",
			Detail: fmt.Sprintf(
				"the answer states %q, but no observation binds %s and %s; a structural query or the call site must show the relationship. Verify it or state the narrower observation",
				boundedClaim(sentence[match[0]:match[1]]), left, right),
		}
	}
	return nil
}

// structuralEndpointIsGeneric filters ordinary prose words that are not code
// endpoints. A dotted endpoint is always concrete enough to audit.
func structuralEndpointIsGeneric(endpoint string) bool {
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	if strings.Contains(endpoint, ".") {
		return false
	}
	switch endpoint {
	case "the", "this", "that", "it", "which", "who", "service", "services",
		"server", "app", "application", "process", "component", "function",
		"method", "code", "package", "module", "file", "handler":
		return true
	default:
		return false
	}
}

// structuralRelationObserved reports a structural observation binding both
// endpoints, or a source call site that contains both and the call syntax.
func structuralRelationObserved(turns []Turn, left, verb, right string) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() {
				continue
			}
			text := strings.ToLower(outcome.Result.Text)
			if !relationEndpointObserved(text, left) || !relationEndpointObserved(text, right) {
				continue
			}
			switch outcome.ToolName {
			case "structure", "symbols", "graph", "refs":
				return true
			}
			if !sourceObservationOutcome(outcome) {
				continue
			}
			if strings.Contains(strings.ToLower(verb), "depend") {
				if strings.Contains(text, "depend") {
					return true
				}
				continue
			}
			if relationCallSiteObserved(text, right) {
				return true
			}
		}
	}
	return false
}

// relationEndpointObserved matches a qualified endpoint by its full spelling,
// or by both its qualifier and member when the observation drops the package.
func relationEndpointObserved(text, endpoint string) bool {
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	if endpoint == "" {
		return false
	}
	if strings.Contains(text, endpoint) {
		return true
	}
	if !strings.Contains(endpoint, ".") {
		return false
	}
	parts := strings.Split(endpoint, ".")
	if len(parts) < 2 {
		return false
	}
	member := parts[len(parts)-1]
	qualifier := parts[len(parts)-2]
	return strings.Contains(text, member) && strings.Contains(text, qualifier)
}

// relationCallSiteObserved reports call syntax for the right endpoint, so a
// file that merely mentions both names is not a call site.
func relationCallSiteObserved(text, endpoint string) bool {
	endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	if endpoint == "" {
		return false
	}
	for _, candidate := range []string{endpoint, lastEndpointSegment(endpoint)} {
		if strings.Contains(text, candidate+"(") {
			return true
		}
	}
	return false
}

// lastEndpointSegment returns the member of a qualified endpoint.
func lastEndpointSegment(endpoint string) string {
	if index := strings.LastIndex(endpoint, "."); index >= 0 {
		return endpoint[index+1:]
	}
	return endpoint
}

// sourceObservationContains reports a source read or source-scoped grep that
// belongs to the named component and contains the behavior stem and, when
// non-empty, the port literal.
func sourceObservationContains(turns []Turn, subject, stem, port string) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || !sourceObservationOutcome(outcome) {
				continue
			}
			if subject != "" && !ComponentMatchesPath(subject, outcomePath(outcome)) {
				continue
			}
			text := strings.ToLower(outcome.Result.Text)
			if !strings.Contains(text, stem) {
				continue
			}
			if port == "" || strings.Contains(text, port) {
				return true
			}
		}
	}
	return false
}

// sourceObservationOutcome reports whether an outcome is a source read or a
// source-scoped grep. Configuration, documentation, and manifests are not
// implementation observations.
func sourceObservationOutcome(outcome ToolOutcome) bool {
	switch outcome.ToolName {
	case "read":
		return isImplementationPath(outcomePath(outcome))
	case "grep":
		return isImplementationSearchPath(outcomePath(outcome))
	default:
		return false
	}
}

// deploymentClaimIssue checks a claim that services are independently
// deployable against deployment artifacts, not merely separate Compose
// service declarations.
func deploymentClaimIssue(sentence string, turns []Turn) *claimIssue {
	match := deploymentClaimPattern.FindString(sentence)
	if match == "" || deploymentObserved(turns) {
		return nil
	}
	return &claimIssue{
		Code: "unsupported_deployment_claim",
		Detail: fmt.Sprintf(
			"the answer says %q, but separate service declarations do not establish independent deployment. Read a deployment artifact or state the narrower topology claim",
			boundedClaim(match)),
	}
}

// speculationClaimIssue checks a path-scoped inference about content the run
// never read. A general inference is left to the model; a claim that a named
// path contains a category must be supported by an observation of that path.
func speculationClaimIssue(sentence string, turns []Turn) *claimIssue {
	match := speculationPattern.FindStringIndex(sentence)
	if match == nil {
		return nil
	}
	ref := nearestReferencePath(sentence, match[0], match[1])
	if ref == "" {
		return nil
	}
	noun := speculativeContentPattern.FindString(sentence)
	if noun == "" || speculationSupported(turns, ref, noun) {
		return nil
	}
	return &claimIssue{
		Code: "unverified_speculation",
		Detail: fmt.Sprintf(
			"the answer speculates that %s contains %q, but no observation of %s supports it; mark it as uninspected or omit it",
			ref, strings.ToLower(noun), ref),
	}
}

// claimIsBounded reports an explicit limitation. An answer that says a claim
// is unverified, not checked, or worth verifying is using the lower-assertion
// option rather than presenting the claim as observed.
//
// "not implemented" and "not used" are deliberately absent: those are exactly
// the concept-absence conclusions this audit exists to bound. A scoped form
// ("no JWT implementation was found in the inspected authentication source")
// is recognized separately by conceptAbsenceIssue.
func claimIsBounded(sentence string) bool {
	lower := strings.ToLower(sentence)
	for _, marker := range []string{
		"not verified", "unverified", "cannot verify", "can't verify",
		"could not verify", "couldn't verify", "not established",
		"no evidence", "would need", "worth verifying", "not evaluated",
		"not inspected", "not checked", "not probed", "in the probes",
		"in this pass", "in this scan", "does not show", "doesn't show",
		"did not show", "didn't show", "does not establish",
		"doesn't establish", "did not establish", "does not prove",
		"doesn't prove", "did not prove", "not shown", "not proven",
		"not found in the inspected", "not found in the searched",
		"not present in the inspected", "in the inspected ",
		"from the inspected ", "the inspected ", "in the inspected files",
		"in the files inspected",
		"in the observed source", "within the inspected",
		"based on the inspected", "so far", "inferred",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// claimIsNonAssertive reports a question, hypothetical, recommendation, or
// plan. None of those is a factual claim about the repository, so the claim
// audit must not turn an honest recommendation into a review boundary.
func claimIsNonAssertive(sentence string) bool {
	trimmed := strings.TrimSpace(sentence)
	if trimmed == "" {
		return true
	}
	if strings.HasSuffix(trimmed, "?") {
		return true
	}
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{
		"if ", "should ", "consider ", "recommend ", "recommended ",
		"we could ", "you could ", "you may ", "you might ", "it would ",
		"it may ", "it might ", "maybe ", "perhaps ", "let me ",
		"let's ", "next, ", "next step", "todo:",
	} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	for _, marker := range []string{
		"i recommend", "we recommend", "would recommend", "should consider",
		"could add", "could implement", "might want", "may want",
		"would be worth", "is worth considering", "if you want",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// claimIsDocumentationAttributed reports a claim explicitly assigned to the
// project's own description. The provenance layer already keeps documentation
// claims distinct from implementation facts; a behavior claim that names the
// README is not an unattributed implementation claim.
func claimIsDocumentationAttributed(sentence string) bool {
	lower := strings.ToLower(sentence)
	for _, marker := range []string{
		"readme", "documentation", "docs", "according to", "described in",
		"stated in", "reported in", "project says", "project states",
		"project claims", "project describes", "repository says",
		"repository states", "repository claims", "repository describes",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// boundedNegativeReport reports whether a negative phrase is scoped to what
// this run's probes surfaced. It is not a claim that the property is absent
// from the repository, unless the same sentence also makes a global scope
// claim.
func boundedNegativeReport(sentence string) bool {
	lower := strings.ToLower(sentence)
	// Contractions carry the same negation the marker scan looks for:
	// "wasn't queried" bounds a claim exactly like "was not queried".
	lower = strings.ReplaceAll(lower, "n't", " not")
	// The veto runs first: "not inspected ... anywhere" is a repository-wide
	// conclusion, and a bounded phrase in the same sentence cannot excuse it.
	if globalNegativeScopePattern.MatchString(lower) {
		return false
	}
	for _, marker := range []string{
		"not evaluated", "not inspected", "not checked", "not probed",
		"not searched", "in the inspected scope", "in the inspected source",
		"in the inspected files", "worth verifying", "would need to check",
		"would need to verify", "would need to inspect", "would need to search",
		"in the probes", "in this pass", "in this scan",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return boundedObservationPattern.MatchString(lower) ||
		boundedRunPattern.MatchString(lower) ||
		boundedScopePattern.MatchString(lower) ||
		boundedUnqueriedPattern.MatchString(lower)
}

// nearestClaimPath returns the path token closest to the metric span. It
// prefers a path before the number ("src/App.tsx (~1200 LOC)") and falls back
// to a path after it ("1200 LOC in src/App.tsx").
func nearestClaimPath(sentence string, start, end int) string {
	const maxDistance = 96
	best := ""
	bestDistance := 1 << 30
	for _, path := range claimPathPattern.FindAllStringIndex(sentence, -1) {
		distance := 0
		switch {
		case path[1] <= start:
			distance = start - path[1]
		case path[0] >= end:
			distance = path[0] - end
		default:
			continue
		}
		if distance < bestDistance {
			bestDistance = distance
			best = sentence[path[0]:path[1]]
		}
	}
	if bestDistance > maxDistance {
		return ""
	}
	return strings.Trim(best, "`'\" ")
}

// nearestReferencePath returns the file or directory coordinate closest to a
// speculation phrase. A sentence may name an entry point and then the file the
// inference is about ("src/main.tsx → src/App.tsx, likely..."); the nearest
// coordinate is the subject of the inference.
func nearestReferencePath(sentence string, start, end int) string {
	const maxDistance = 160
	type candidate struct {
		start, end int
		value      string
	}
	var candidates []candidate
	files := claimPathPattern.FindAllStringIndex(sentence, -1)
	for _, path := range files {
		candidates = append(candidates, candidate{path[0], path[1], sentence[path[0]:path[1]]})
	}
	for _, dir := range claimDirectoryPattern.FindAllStringIndex(sentence, -1) {
		// The directory prefix of a named file is less specific than the file
		// itself: "src/" must not win over "src/App.tsx".
		filePrefix := false
		for _, path := range files {
			if path[0] == dir[0] {
				filePrefix = true
				break
			}
		}
		if filePrefix {
			continue
		}
		value := strings.Trim(sentence[dir[0]:dir[1]], "`'\" ")
		segments := strings.Split(strings.Trim(value, "/"), "/")
		// A single-segment token such as "schema/" or "routes/" in
		// "likely schema/edge functions" is the speculative category, not the
		// path being described. A real nested directory keeps its parent.
		if len(segments) == 1 && speculativeContentPattern.MatchString(segments[0]) {
			continue
		}
		candidates = append(candidates, candidate{dir[0], dir[1], value})
	}
	best := ""
	bestDistance := 1 << 30
	bestLength := 0
	for _, item := range candidates {
		distance := 0
		switch {
		case item.end <= start:
			distance = start - item.end
		case item.start >= end:
			distance = item.start - end
		default:
			continue
		}
		length := item.end - item.start
		if distance < bestDistance || (distance == bestDistance && length > bestLength) {
			bestDistance = distance
			bestLength = length
			best = item.value
		}
	}
	if bestDistance > maxDistance {
		return ""
	}
	return strings.Trim(best, "`'\" ")
}

// observedReadLineCount returns the total line count reported by a successful
// read of path.
func observedReadLineCount(turns []Turn, path string) (int, bool) {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.ToolName != "read" || outcome.Result.IsError || outcome.Refused() {
				continue
			}
			if !claimPathsMatch(outcomePath(outcome), path) {
				continue
			}
			if total := claimDetailInt(outcome.Result.Details, "totalLines"); total > 0 {
				return total, true
			}
		}
	}
	return 0, false
}

// commandLineCountObserved reports a line count measured by a command.
func commandLineCountObserved(turns []Turn, claimed int) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.ToolName != "bash" || outcome.Result.IsError || !outcome.Result.Executes {
				continue
			}
			command := strings.ToLower(proposalArgString(outcome.Args, "command"))
			if !strings.Contains(command, "wc ") && !strings.Contains(command, "cloc") &&
				!strings.Contains(command, "tokei") && !strings.Contains(command, "scc ") &&
				!strings.Contains(command, "sloc") {
				continue
			}
			for _, raw := range lineCountNumberPattern.FindAllString(outcome.Result.Text, -1) {
				if value := claimCount(raw); value > 0 && lineCountMatches(value, claimed) {
					return true
				}
			}
		}
	}
	return false
}

var lineCountNumberPattern = regexp.MustCompile(`\b\d[\d,]*\b`)

// lineCountMatches accepts an approximation within a fifth of the claim (or
// 50 lines, whichever is larger). A gross contradiction such as 40 vs 1200 is
// not an approximation.
func lineCountMatches(observed, claimed int) bool {
	tolerance := claimed / 5
	if tolerance < 50 {
		tolerance = 50
	}
	difference := observed - claimed
	if difference < 0 {
		difference = -difference
	}
	return difference <= tolerance
}

// claimCount parses a positive decimal count with optional commas.
func claimCount(raw string) int {
	value, err := strconv.Atoi(strings.ReplaceAll(strings.TrimSpace(raw), ",", ""))
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

// claimCountAllowZero parses a non-negative decimal count. A zero test count is
// a valid inventory observation, not a missing value.
func claimCountAllowZero(raw string) (int, bool) {
	value, err := strconv.Atoi(strings.ReplaceAll(strings.TrimSpace(raw), ",", ""))
	if err != nil || value < 0 {
		return 0, false
	}
	return value, true
}

// deploymentObserved reports a read deployment artifact. A Dockerfile or a
// Kubernetes/Helm manifest is a deployment boundary; a Compose service entry
// alone is topology.
func deploymentObserved(turns []Turn) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || isDocumentationOutcome(outcome) {
				continue
			}
			path := strings.ToLower(filepath.ToSlash(outcomePath(outcome)))
			base := filepath.Base(path)
			if base == "dockerfile" || strings.HasPrefix(base, "dockerfile.") {
				return true
			}
			for _, prefix := range []string{"k8s/", "deploy/", "deployment/", "helm/", "charts/"} {
				if strings.HasPrefix(path, prefix) || strings.Contains(path, "/"+prefix) {
					return true
				}
			}
			text := strings.ToLower(outcome.Result.Text)
			if strings.Contains(text, "kind: deployment") || strings.Contains(text, "apiversion: apps/v1") {
				return true
			}
		}
	}
	return false
}

// observedMechanismContains reports a successful configuration or source
// observation whose text contains one of the markers. Documentation prose is
// not a mechanism.
func observedMechanismContains(turns []Turn, markers ...string) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || isDocumentationOutcome(outcome) {
				continue
			}
			text := strings.ToLower(outcome.Result.Text)
			for _, marker := range markers {
				if strings.Contains(text, marker) {
					return true
				}
			}
		}
	}
	return false
}

// speculationSupported reports whether the observation scoped to ref contains
// the speculative content noun.
func speculationSupported(turns []Turn, ref, noun string) bool {
	stem := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(noun), "s"))
	if stem == "" {
		return false
	}
	reference := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(ref)), "/")
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() || isDocumentationOutcome(outcome) {
				continue
			}
			path := strings.TrimSuffix(strings.ToLower(filepath.ToSlash(outcomePath(outcome))), "/")
			text := strings.ToLower(outcome.Result.Text)
			if !pathScopeCovers(path, reference) && !strings.Contains(text, reference) {
				continue
			}
			if strings.Contains(text, stem) {
				return true
			}
		}
	}
	return false
}

// pathScopeCovers reports whether an observation path is the reference or an
// ancestor listing of it.
func pathScopeCovers(observed, reference string) bool {
	observed = strings.Trim(strings.TrimSpace(filepath.ToSlash(observed)), "/")
	reference = strings.Trim(strings.TrimSpace(filepath.ToSlash(reference)), "/")
	if observed == "" || reference == "" {
		return false
	}
	if observed == reference ||
		strings.HasPrefix(reference, observed+"/") ||
		strings.HasPrefix(observed, reference+"/") {
		return true
	}
	return filepath.Base(observed) == filepath.Base(reference)
}

// isImplementationPath reports whether a read path is source code rather than
// documentation, a manifest, configuration, or generated content.
func isImplementationPath(path string) bool {
	return ClassifyPath(path) == PathSource
}

// isImplementationSearchPath reports whether a grep path is a source scope.
// A grep normally names a directory, so the extension check cannot apply. A
// repository-root search is mixed content, not a source-only observation.
func isImplementationSearchPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" || path == "." {
		return false
	}
	switch ClassifyPath(path) {
	case PathDocumentation, PathManifest, PathConfiguration, PathGenerated:
		return false
	default:
		return true
	}
}

// claimPathsMatch compares a read path with the answer's coordinate. The tools
// may report an absolute path while the answer names a repository-relative one.
func claimPathsMatch(observed, claimed string) bool {
	observed = strings.Trim(strings.ToLower(filepath.ToSlash(strings.TrimSpace(observed))), "/")
	claimed = strings.Trim(strings.ToLower(filepath.ToSlash(strings.TrimSpace(claimed))), "/")
	if observed == "" || claimed == "" {
		return false
	}
	if observed == claimed || strings.HasSuffix(observed, "/"+claimed) {
		return true
	}
	if !strings.Contains(claimed, "/") && filepath.Base(observed) == claimed {
		return true
	}
	return false
}

// behaviorStem returns the source token a claim's behavior word would appear
// as in an implementation observation.
func behaviorStem(phrase string) string {
	word := strings.ToLower(strings.Fields(phrase)[0])
	switch {
	case strings.HasPrefix(word, "validat"):
		return "validat"
	case strings.HasPrefix(word, "publish"):
		return "publish"
	case strings.HasPrefix(word, "consum"):
		return "consum"
	case strings.HasPrefix(word, "subscrib"):
		return "subscrib"
	case strings.HasPrefix(word, "fulfil"):
		return "fulfil"
	case strings.HasPrefix(word, "call"):
		return "call"
	case strings.HasPrefix(word, "send"):
		return "send"
	case strings.HasPrefix(word, "execut"):
		return "execut"
	case strings.HasPrefix(word, "read"):
		return "read"
	case strings.HasPrefix(word, "writ"):
		return "writ"
	case strings.HasPrefix(word, "rout"):
		return "rout"
	case strings.HasPrefix(word, "listen"):
		return "listen"
	case strings.HasPrefix(word, "process"):
		return "process"
	case strings.HasPrefix(word, "handl"):
		return "handl"
	case strings.HasPrefix(word, "stor"):
		return "stor"
	case strings.HasPrefix(word, "connect"):
		return "connect"
	case strings.HasPrefix(word, "run"):
		return "run"
	default:
		return ""
	}
}

// autoApplyClaim reports whether the matched behavior phrase is an
// auto-application mechanism.
func autoApplyClaim(phrase string) bool {
	return strings.HasPrefix(strings.ToLower(phrase), "auto")
}

// boundedClaim trims a matched claim to a readable length for the review
// reason.
func boundedClaim(claim string) string {
	claim = strings.TrimSpace(claim)
	runes := []rune(claim)
	if len(runes) <= 80 {
		return claim
	}
	return strings.TrimSpace(string(runes[:77])) + "..."
}

// outcomePath returns the path a tool observation names.
func outcomePath(outcome ToolOutcome) string {
	if path := detailString(outcome.Result.Details, "path"); path != "" {
		return path
	}
	return proposalArgString(outcome.Args, "path")
}

// isDocumentationOutcome reports a prose observation. Documentation can
// support an attributed claim, but it is not implementation or configuration
// evidence.
func isDocumentationOutcome(outcome ToolOutcome) bool {
	return ClassifyPath(outcomePath(outcome)) == PathDocumentation
}

// claimDetailInt reads an integer field from a tool result's Details map.
func claimDetailInt(details any, key string) int {
	values, ok := details.(map[string]any)
	if !ok {
		return 0
	}
	switch value := values[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

// stripFencedCode removes quoted tool output so a code block cannot be read as
// a prose claim.
func stripFencedCode(answer string) string {
	var out strings.Builder
	inFence := false
	for _, line := range strings.Split(answer, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

// dedupeClaimIssues preserves the first occurrence of each issue.
func dedupeClaimIssues(issues []claimIssue) []claimIssue {
	seen := map[string]bool{}
	var out []claimIssue
	for _, issue := range issues {
		key := issue.Code + "\x00" + issue.Detail
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, issue)
	}
	return out
}
