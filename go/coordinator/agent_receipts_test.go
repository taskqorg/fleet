package main

import "testing"

func TestReceiptAuthorityRevocation(t *testing.T) {
	before := &Person{Email: "a", Roles: []string{"operator"}}
	after := *before
	if !receiptAuthority(&after, before) {
		t.Fatal("unchanged authority denied")
	}
	after.Roles = nil
	if receiptAuthority(&after, before) {
		t.Fatal("revoked role exposes receipt")
	}
	before.Owner = true
	if receiptAuthority(&after, before) {
		t.Fatal("former owner exposes receipt")
	}
	after.Owner = true
	if !receiptAuthority(&after, before) {
		t.Fatal("existing privileged behavior lost")
	}
}
