package main

import (
	"reflect"
	"testing"

	"fleet/contract"
)

// A host body key that names hostId only case-insensitively would bypass readBody's exact-key host check.
func TestExactKeysHostBodies(t *testing.T) {
	ty := reflect.TypeOf(contract.ChildReceipt{})
	if hostKeys([]byte(`{"hostId":"a","HostID":"b"}`), ty) == nil {
		t.Fatal("HostID accepted")
	}
	if hostKeys([]byte(`{"delegation":{"ProfileID":"x"}}`), ty) == nil {
		t.Fatal("nested ProfileID accepted")
	}
	full := `"hostId":"a","executionId":"e","commandId":"c","delegation":{"input":"i","profileId":"p","requestId":"r"}`
	if err := hostKeys([]byte(`{`+full+`,"extra":1}`), ty); err != nil {
		t.Fatal(err)
	}
	if hostKeys([]byte(`{"hostId":"a"}`), ty) == nil {
		t.Fatal("missing executionId accepted")
	}
	cat := reflect.TypeOf(contract.AccountCatalogue{})
	if hostKeys([]byte(`{"accountId":"a","bindings":[{"name":"x","NAME":"y","executable":"/e","environment":{},"behaviorApproved":true}]}`), cat) == nil {
		t.Fatal("case variant in a list element accepted")
	}
	if hostKeys([]byte(`{"accountId":"a","bindings":[{"name":"x","executable":"/e","behaviorApproved":true}]}`), cat) == nil {
		t.Fatal("binding without environment accepted")
	}
}

// An operation stores incomplete nested content (definition_draft_put): only top-level keys are required; null is absent.
func TestExactKeysNested(t *testing.T) {
	ty := reflect.TypeOf(contract.ChildReceipt{})
	if err := exactKeys([]byte(`{"hostId":"a","executionId":"e","commandId":"c","delegation":{}}`), ty); err != nil {
		t.Fatal(err)
	}
	if hostKeys([]byte(`{"accountId":"a","bindings":null}`), reflect.TypeOf(contract.AccountCatalogue{})) != nil {
		t.Fatal("null list refused by the key check (Validate owns it)")
	}
}
