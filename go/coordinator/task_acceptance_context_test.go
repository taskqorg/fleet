package main

import (
	"strings"
	"testing"
	"unicode/utf8"

	"fleet/contract"
	"fleet/coordinator/db"
)

// Acceptance previews identify only the offered result and keep absent evidence unknown.
func TestAcceptanceContext(t *testing.T) {
	row := db.TaskAcceptanceContextRow{TaskCode: ptr("T-FLT-138"), Report: "## Summary\nAdded result context.\n\n- Checks passed: app and Go."}
	pr := deliveryMergedPR{Repo: "taskqorg/fleet", Number: 42, Release: "v1.2.3", HeadSHA: "offered-head"}
	withDelivery := acceptanceContext(row, pr, contract.PublishingHostRecord{})
	if deref(withDelivery.TaskCode) != "T-FLT-138" || deref(withDelivery.Summary) != "Added result context." ||
		deref(withDelivery.Verification) != "Checks passed: app and Go." || deref(withDelivery.Release) != "v1.2.3" ||
		withDelivery.PullRequest == nil || withDelivery.PullRequest.Number != 42 || withDelivery.PullRequest.Repo != pr.Repo {
		t.Fatalf("unexpected context with linked delivery: %+v", withDelivery)
	}

	artifact := acceptanceContext(db.TaskAcceptanceContextRow{ArtifactSummary: "Created a design brief."}, deliveryMergedPR{}, contract.PublishingHostRecord{})
	if deref(artifact.Summary) != "Created a design brief." || artifact.PullRequest != nil || artifact.Release != nil || artifact.Verification != nil {
		t.Fatalf("unexpected context with only an artifact: %+v", artifact)
	}
	noEvidence := acceptanceContext(db.TaskAcceptanceContextRow{}, deliveryMergedPR{}, contract.PublishingHostRecord{})
	if noEvidence.Summary != nil || noEvidence.Verification != nil || noEvidence.PullRequest != nil || noEvidence.Release != nil {
		t.Fatalf("unexpected context without evidence: %+v", noEvidence)
	}

	pub := contract.PublishingHostRecord{Number: 53, Repo: "taskqorg/fleet", HeadSha: "other-head", Title: "Improve result context", ChecksState: ptr("success")}
	published := acceptanceContext(db.TaskAcceptanceContextRow{}, deliveryMergedPR{}, pub)
	if published.PullRequest == nil || published.PullRequest.Number != 53 || published.Release != nil ||
		deref(published.Summary) != pub.Title || deref(published.Verification) != "Pull request checks passed." {
		t.Fatalf("unexpected context from publishing receipt: %+v", published)
	}
	otherPR := acceptanceContext(db.TaskAcceptanceContextRow{}, pr, pub)
	if otherPR.Verification != nil {
		t.Fatal("verification from a different pull request was attached to the offer")
	}
}

func TestAcceptanceReportPreview(t *testing.T) {
	summary, verification := acceptanceReport(strings.Repeat("海 ", 150) + "\nVerified: smoke and typecheck.")
	if utf8.RuneCountInString(summary) > 241 || !strings.HasSuffix(summary, "…") || verification != "Verified: smoke and typecheck." {
		t.Fatalf("unexpected summary or verification: %q, %q", summary, verification)
	}
	summary, verification = acceptanceReport("Added repeat-submission coverage. Go and app merge queue checks passed. More details in the result.")
	if summary != "Added repeat-submission coverage." || verification != "Go and app merge queue checks passed." {
		t.Fatalf("unexpected plain-text summary or verification: %q, %q", summary, verification)
	}
}

func TestAcceptanceRetainedRelease(t *testing.T) {
	const tag = "v1.2.3"
	history := map[int64]db.TaskAcceptancePRHistoryRow{42: {Number: 42, HeadSha: "observed-head", Release: tag}}
	row := db.TaskAcceptanceContextRow{ProjectID: ptr(ciProject), Report: "PR #42 shipped in v1.2.3."}
	for _, tc := range []struct {
		name, report string
		pub          contract.PublishingHostRecord
		want         string
	}{
		{name: "same observed head", pub: contract.PublishingHostRecord{Provider: "github", Host: "github.com", Repo: "taskqorg/fleet", Number: 42, HeadSha: "observed-head"}, want: tag},
		{name: "moved head", pub: contract.PublishingHostRecord{Provider: "github", Host: "github.com", Repo: "taskqorg/fleet", Number: 42, HeadSha: "other-head"}},
		{name: "corroborated report", report: row.Report, want: tag},
		{name: "different release", report: "PR #42 shipped in v1.2.3.4."},
		{name: "ambiguous report", report: "PR #42 and PR #43 shipped in v1.2.3."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row.Report = tc.report
			if got := acceptanceRetainedRelease(row, deliveryMergedPR{}, tc.pub, history); got.Release != tc.want {
				t.Fatalf("release = %q, want %q", got.Release, tc.want)
			}
		})
	}
}
