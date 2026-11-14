package agent

import (
	"fmt"
	"strings"
)

// A path citation is an evidence reference: the answer points the reader at the
// file that is supposed to establish its claim.
//
// "Search: FlexSearch with a build-time prebuilt index (lib/search.ts)" reads as
// proof because the coordinate looks inspected, but a listing proves only that
// the path exists. The run never opened the file, so the answer cannot tell a
// wired mechanism from a declared dependency — and the reader has no way to
// notice. This audit is deliberately narrow: it fires on an explicit citation
// form (a parenthesized or bracketed source file), never on a path mentioned in
// prose, so a route table or an inventory answer is not forced into a review.

// citationClaimIssue checks every source file the answer cites as evidence.
func citationClaimIssue(sentence string, turns []Turn) *claimIssue {
	for _, match := range claimPathPattern.FindAllStringIndex(sentence, -1) {
		path := strings.Trim(sentence[match[0]:match[1]], "`'\" ")
		if !isImplementationPath(path) || !citedAsEvidence(sentence, match[0]) {
			continue
		}
		if citationSourceObserved(turns, path) {
			continue
		}
		return &claimIssue{
			Code: "unsupported_source_citation",
			Detail: fmt.Sprintf(
				"the answer cites %s as evidence, but the run never opened it; a listing proves the path exists, not what it implements. Read it or present the path as an unverified pointer",
				path),
		}
	}
	return nil
}

// citedAsEvidence reports whether the path is presented in an explicit citation
// form: parentheses or brackets, optionally inside inline code.
func citedAsEvidence(sentence string, start int) bool {
	i := start - 1
	for i >= 0 && (sentence[i] == '`' || sentence[i] == ' ' || sentence[i] == '\t') {
		i--
	}
	return i >= 0 && (sentence[i] == '(' || sentence[i] == '[')
}

// citationSourceObserved reports a source observation of the cited path. A
// structural coordinate or a manifest entry is not source content: it says the
// file exists, not what the file does.
func citationSourceObserved(turns []Turn, path string) bool {
	for _, turn := range turns {
		for _, outcome := range turn.Results {
			if outcome.Result.IsError || outcome.Refused() {
				continue
			}
			if !claimPathsMatch(outcomePath(outcome), path) {
				continue
			}
			if sourceObservationOutcome(outcome) {
				return true
			}
		}
	}
	return false
}
