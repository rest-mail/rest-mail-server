package mime

// Item 3 (issue #201): EstimateSize summed only the top-level body parts, so a
// nested multipart (multipart/mixed → multipart/alternative → text parts)
// undercounted — the leaf content two levels down was never added. That let a
// message far larger than the configured limit slip past size_check.

import (
	"strings"
	"testing"

	"github.com/restmail/restmail/internal/pipeline"
)

// TestEstimateSize_RecursesNestedParts builds a body whose only sizeable content
// lives two levels deep and asserts EstimateSize accounts for it.
func TestEstimateSize_RecursesNestedParts(t *testing.T) {
	deepText := strings.Repeat("A", 5000)
	deepHTML := strings.Repeat("B", 7000)

	email := &pipeline.EmailJSON{
		Body: pipeline.Body{
			ContentType: "multipart/mixed",
			// No content at the top level — everything is in the nested subtree.
			Parts: []pipeline.Body{
				{
					ContentType: "multipart/alternative",
					Parts: []pipeline.Body{
						{ContentType: "text/plain", Content: deepText},
						{ContentType: "text/html", Content: deepHTML},
					},
				},
			},
		},
	}

	got := EstimateSize(email)
	want := int64(len(deepText) + len(deepHTML))
	if got < want {
		t.Errorf("EstimateSize = %d, want >= %d (nested part content not counted)", got, want)
	}
}
