package main

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"fleet/contract"
	"fleet/coordinator/db"
)

// acceptanceContexts composes evidence in the same scoped read as the question rows. Old offers already
// retain their execution, selected artifacts and optional delivery snapshot.
func acceptanceContexts(q *Request, questions []db.TaskQuestionsRow) (map[string]*contract.AcceptanceContext, error) {
	var ids, executionIDs []string
	executionByQuestion := make(map[string]string)
	for _, row := range questions {
		if row.Kind == db.QuestionKindAcceptance {
			ids = append(ids, row.ID)
			if row.ExecutionID != nil {
				executionIDs = append(executionIDs, *row.ExecutionID)
				executionByQuestion[row.ID] = *row.ExecutionID
			}
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	contexts, err := q.db().TaskAcceptanceContext(q.ctx(), ids)
	if err != nil {
		return nil, err
	}
	publishing := make(map[string]contract.PublishingHostRecord)
	if len(executionIDs) > 0 {
		rows, err := q.db().ThreadPublishingResults(q.ctx(), db.ThreadPublishingResultsParams{Ids: executionIDs, Changed: []string{}})
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			var observed contract.PublishingHostRecord
			if json.Unmarshal(row.Value, &observed) == nil {
				publishing[row.ExecutionID] = observed
			}
		}
	}
	history, err := acceptancePRHistory(q, contexts, publishing, executionByQuestion)
	if err != nil {
		return nil, err
	}
	delivered := snapshotDeliveredPRs()
	result := make(map[string]*contract.AcceptanceContext, len(contexts))
	for _, row := range contexts {
		var delivery deliveryMergedPR
		_ = json.Unmarshal(row.Delivery, &delivery)
		executionID := executionByQuestion[row.ID]
		pub := publishing[executionID]
		if deref(pub.SourceExecutionId) != executionID {
			pub.ChecksState = nil
		}
		if delivery.Number == 0 && publicGitHubPublishing(pub) && pub.HeadSha != "" {
			for _, observed := range delivered {
				if strings.EqualFold(observed.Repo, pub.Repo) && observed.Number == pub.Number && observed.HeadSHA == pub.HeadSha {
					delivery = observed
					break
				}
			}
		}
		delivery = acceptanceRetainedRelease(row, delivery, pub, history)
		result[row.ID] = acceptanceContext(row, delivery, pub)
	}
	return result, nil
}

func acceptancePRHistory(q *Request, rows []db.TaskAcceptanceContextRow, publishing map[string]contract.PublishingHostRecord, executionByQuestion map[string]string) (map[int64]db.TaskAcceptancePRHistoryRow, error) {
	var numbers []int64
	for _, row := range rows {
		pub := publishing[executionByQuestion[row.ID]]
		if number := acceptanceHistoryNumber(row, pub); number > 0 {
			numbers = append(numbers, number)
		}
	}
	if len(numbers) == 0 {
		return nil, nil
	}
	found, err := q.db().TaskAcceptancePRHistory(q.ctx(), db.TaskAcceptancePRHistoryParams{ProjectID: ciProject, Numbers: numbers})
	if err != nil {
		return nil, err
	}
	history := make(map[int64]db.TaskAcceptancePRHistoryRow, len(found))
	for _, row := range found {
		history[row.Number] = row
	}
	return history, nil
}

func publicGitHubPublishing(record contract.PublishingHostRecord) bool {
	return record.Provider == "github" && strings.EqualFold(record.Host, "github.com")
}

func acceptanceHistoryNumber(row db.TaskAcceptanceContextRow, published contract.PublishingHostRecord) int64 {
	if deref(row.ProjectID) != ciProject {
		return 0
	}
	if published.Number > 0 {
		if publicGitHubPublishing(published) && strings.EqualFold(published.Repo, ciOwner+"/"+ciRepo) && published.HeadSha != "" {
			return published.Number
		}
		return 0
	}
	// Some workers record delivery in the report without a source-bound publishing receipt.
	// A relative PR number can name Fleet's history only in the Fleet project and when unambiguous.
	var number int64
	for _, match := range acceptanceReportedPR.FindAllStringSubmatch(row.Report, -1) {
		n, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil || number != 0 && n != number {
			return 0
		}
		number = n
	}
	return number
}

func acceptanceRetainedRelease(row db.TaskAcceptanceContextRow, delivery deliveryMergedPR, published contract.PublishingHostRecord, history map[int64]db.TaskAcceptancePRHistoryRow) deliveryMergedPR {
	if delivery.Release != "" {
		return delivery
	}
	retained, ok := history[acceptanceHistoryNumber(row, published)]
	if !ok || retained.Release == "" {
		return delivery
	}
	if published.Number > 0 && retained.HeadSha == published.HeadSha &&
		(delivery.Number == 0 || delivery.Number == published.Number && delivery.HeadSHA == published.HeadSha) {
		delivery.Release = retained.Release
	}
	if delivery.Number == 0 && published.Number == 0 {
		for _, word := range strings.FieldsFunc(row.Report, func(r rune) bool {
			return unicode.IsSpace(r) || strings.ContainsRune("[]()`", r)
		}) {
			if strings.TrimRight(word, ".,;:") == retained.Release {
				return deliveryMergedPR{Repo: ciOwner + "/" + ciRepo, Number: retained.Number, Release: retained.Release}
			}
		}
	}
	return delivery
}

var (
	acceptanceReportedPR = regexp.MustCompile(`(?i)\bPR\s+#([0-9]+)\b`)
	acceptanceCheckLine  = regexp.MustCompile(`(?i)^(?:checks(?: passed| failed)?|verification|verified|tests(?: passed| failed)?)\s*:\s*`)
	acceptanceCheckFact  = regexp.MustCompile(`(?i)\b(?:production|checks|tests|CI|code-review)\b.*\b(?:passed|failed)\b`)
)

func acceptanceContext(row db.TaskAcceptanceContextRow, delivery deliveryMergedPR, published contract.PublishingHostRecord) *contract.AcceptanceContext {
	summary, verification := acceptanceReport(row.Report)
	if summary == "" {
		summary = acceptanceExcerpt(firstString(row.ArtifactSummary, delivery.Title, published.Title))
	}
	var request *contract.AcceptancePullRequest
	switch {
	case delivery.Number > 0 && delivery.Repo != "":
		request = &contract.AcceptancePullRequest{Repo: delivery.Repo, Number: delivery.Number, Url: optStr(delivery.URL)}
	case published.Number > 0 && published.Repo != "":
		request = &contract.AcceptancePullRequest{Repo: published.Repo, Number: published.Number, Url: optStr(published.Url)}
	}
	if verification == "" && (delivery.Number == 0 || (strings.EqualFold(delivery.Repo, published.Repo) &&
		delivery.Number == published.Number && delivery.HeadSHA != "" && delivery.HeadSHA == published.HeadSha)) {
		switch strings.ToLower(deref(published.ChecksState)) {
		case "success", "succeeded", "passed":
			verification = "Pull request checks passed."
		case "failure", "failed":
			verification = "Pull request checks failed."
		}
	}
	if verification == "" && row.RunSucceeded {
		verification = "The offered run completed successfully."
	}
	return &contract.AcceptanceContext{
		TaskCode: row.TaskCode, Summary: optStr(summary), Verification: optStr(verification),
		PullRequest: request, Release: optStr(delivery.Release),
	}
}

func acceptanceReport(report string) (summary, verification string) {
	fence := false
	for line := range strings.SplitSeq(report, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fence = !fence
			continue
		}
		if fence || line == "" || nowHeading.MatchString(line) {
			continue
		}
		line = nowMarker.ReplaceAllString(line, "")
		line = nowLink.ReplaceAllString(line, "$1")
		line = strings.NewReplacer("**", "", "__", "", "`", "").Replace(line)
		if prefix := acceptanceCheckLine.FindStringIndex(line); prefix != nil {
			if verification == "" && strings.TrimSpace(line[prefix[1]:]) != "" {
				verification = acceptanceExcerpt(line)
			}
			continue
		}
		start := 0
		for _, end := range append(nowEnd.FindAllStringIndex(line, -1), []int{len(line), len(line)}) {
			sentence := strings.TrimSpace(line[start:end[1]])
			start = end[1]
			if sentence == "" {
				continue
			}
			if summary == "" {
				summary = acceptanceExcerpt(sentence)
			} else if verification == "" && acceptanceCheckFact.MatchString(sentence) {
				verification = acceptanceExcerpt(sentence)
			}
		}
	}
	return
}

// A preview carries one line of evidence. The linked task and results hold the full report.
func acceptanceExcerpt(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	const previewCharacters = 240
	if runes := []rune(s); len(runes) > previewCharacters {
		preview := string(runes[:previewCharacters])
		if at := strings.LastIndexByte(preview, ' '); at > 0 {
			return preview[:at] + "…"
		}
		// A single technical identifier or URL cannot provide a meaningful preview.
		return ""
	}
	return s
}
