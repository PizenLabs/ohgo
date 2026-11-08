package tui

import (
	"strings"
	"testing"
)

// A review reason describes the current review boundary. A running or finished
// task may still carry the durable historical value (older checkpoints), but
// the UI must not present it as a live warning.
func TestTaskSurfacesHideReviewReasonOutsideReview(t *testing.T) {
	for _, status := range []string{"RUNNING", "PAUSED", "COMPLETED", "FAILED", "CANCELLED"} {
		t.Run(status, func(t *testing.T) {
			resume := TaskResume{
				ID:           "t1",
				Status:       status,
				Objective:    "fix the parser",
				ReviewReason: "the change was not verified",
			}
			if text := taskStatusText(resume, false); strings.Contains(strings.ToLower(text), "needs review") {
				t.Fatalf("%s status still presents a review warning: %q", status, text)
			}
			projection := TaskProjection{
				ID:           "t1",
				Status:       status,
				Objective:    "fix the parser",
				ReviewReason: "the change was not verified",
			}
			for _, insight := range sidebarInsights(&projection) {
				if strings.HasPrefix(insight, "review:") {
					t.Fatalf("%s sidebar still presents a review warning: %q", status, insight)
				}
			}
		})
	}
}

// While the review is open the reason is the most useful row and must remain.
func TestTaskSurfacesShowCurrentReviewReason(t *testing.T) {
	resume := TaskResume{
		ID:           "t1",
		Status:       "NEEDS_REVIEW",
		Objective:    "fix the parser",
		ReviewReason: "the change was not verified",
	}
	if text := taskStatusText(resume, false); !strings.Contains(text, "the change was not verified") {
		t.Fatalf("review status lost its reason: %q", text)
	}
	projection := TaskProjection{
		ID:           "t1",
		Status:       "NEEDS_REVIEW",
		Objective:    "fix the parser",
		ReviewReason: "the change was not verified",
	}
	found := false
	for _, insight := range sidebarInsights(&projection) {
		if strings.Contains(insight, "the change was not verified") {
			found = true
		}
	}
	if !found {
		t.Fatal("the sidebar lost the current review reason")
	}
}
