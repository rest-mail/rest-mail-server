package pipeline

import "testing"

func TestWithDMARCReportsStep(t *testing.T) {
	for _, steps := range [][]FilterConfig{
		nil,
		{{Name: "dmarc_reports_received", Enabled: false}},
		{{Name: "dmarc_reports_received"}, {Name: "extract_attachments"}, {Name: "dmarc_check"}, {Name: "dmarc_reports_received"}},
		DefaultInboundPipeline(1).Filters,
	} {
		got := WithDMARCReportsStep(steps)
		count, reportAt, authAt, extractAt := 0, -1, -1, -1
		for index, step := range got {
			switch step.Name {
			case "dmarc_reports_received":
				count++
				reportAt = index
				if !step.Enabled || !step.Unskippable {
					t.Fatal("report intake can be disabled or skipped")
				}
			case "dmarc_check":
				authAt = index
			case "extract_attachments":
				extractAt = index
			}
		}
		if count != 1 || reportAt <= authAt || extractAt >= 0 && reportAt >= extractAt {
			t.Fatalf("bad report step order: %+v", got)
		}
	}
}
