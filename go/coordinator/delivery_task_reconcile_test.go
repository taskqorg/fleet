package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Requirement: deliveryMergedPR JSON payload stored on acceptance question data and audit_log omits internal-only flags.
// Risk: internal fields like Delivered leak into public question data or audit records.
func TestDeliveryTaskReconcileAuditEntry(t *testing.T) {
	// Sanity check JSON round-trip of deliveryMergedPR stored inside questionData & audit_log.
	pr := deliveryMergedPR{Repo: "taskqorg/fleet", Number: 787, Title: "Fix", URL: "https://github.com/taskqorg/fleet/pull/787", MergedAt: "2026-11-23T14:53:16Z", Stage: "installed", Release: "v1.261123.1", DeliveredAt: "2026-11-23T15:10:00Z", Delivered: true}
	raw, err := json.Marshal(pr)
	if err != nil || strings.Contains(string(raw), `"Delivered"`) {
		t.Fatalf("unexpected deliveryMergedPR marshal: %s err=%v", raw, err)
	}
}
