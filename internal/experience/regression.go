package experience

// RegressionFor returns the deterministic regression case linked to one lesson
// kind.
//
// The mapping is intentionally static and closed: every lesson kind the
// extractor can produce must have a regression, so a promoted record can always
// name the test that defends its behaviour. The runtime stores the reference;
// the repository's test suite is the executor. This is why no LLM is required
// to generate regression coverage for the initial lesson classes.
func RegressionFor(lessonKind string) RegressionCase {
	switch lessonKind {
	case EventRepeatedProbe:
		return RegressionCase{
			Kind:        EventRepeatedProbe,
			Name:        "TestRepeatedProbeIncidentIsDeterministic",
			Description: "an identical successful probe without a mutation is recorded once and becomes a duplicate warning, never a hard refusal",
		}
	case EventRecoverableFailure:
		return RegressionCase{
			Kind:        EventRecoverableFailure,
			Name:        "TestRecoverableFailureIsNonMaterialWhenEquivalentEvidenceExists",
			Description: "a failed directory read covered by a listing is non-material and does not force a review",
		}
	case EventMaterialFailure:
		return RegressionCase{
			Kind:        EventMaterialFailure,
			Name:        "TestMaterialFailureRemainsBlocking",
			Description: "a required failure with no equivalent observation remains a material failure",
		}
	case EventSuccessfulShortcut:
		return RegressionCase{
			Kind:        EventSuccessfulShortcut,
			Name:        "TestSuccessfulShortcutCandidateIsPromotedAfterValidation",
			Description: "a low-cost successful path becomes a preferred-order candidate only after validation",
		}
	case EventExpensivePath:
		return RegressionCase{
			Kind:        EventExpensivePath,
			Name:        "TestExpensivePathProducesACostedCandidate",
			Description: "a successful but expensive path records a lower-cost preference without discarding evidence",
		}
	case EventEvidenceGap:
		return RegressionCase{
			Kind:        EventEvidenceGap,
			Name:        "TestEvidenceGapCannotBecomePromotedExperience",
			Description: "an evidence gap produces guidance but never becomes evidence or a completion claim",
		}
	case EventStaleReview:
		return RegressionCase{
			Kind:        EventStaleReview,
			Name:        "TestRepeatedReviewDoesNotPromoteStaleLesson",
			Description: "a repeated review boundary is recorded as stale and cannot be promoted into a path preference",
		}
	case EventHandoffRedundancy:
		return RegressionCase{
			Kind:        EventHandoffRedundancy,
			Name:        "TestHandoffRedundancyIsRecorded",
			Description: "re-probing evidence already carried across a BUILD/FUZZ handoff is detected without changing authority",
		}
	case EventContextGrowth:
		return RegressionCase{
			Kind:        EventContextGrowth,
			Name:        "TestContextGrowthIsBounded",
			Description: "a large projected request produces a bounded context lesson, never transcript retention",
		}
	case EventAuthorityRefusal:
		return RegressionCase{
			Kind:        EventAuthorityRefusal,
			Name:        "TestAuthorityRefusalCannotBecomePolicy",
			Description: "a refused call is recorded as path guidance; AES cannot alter the authority decision",
		}
	default:
		return RegressionCase{}
	}
}
