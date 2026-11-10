package main

import "testing"

// Requirement: S3 requests to Garage are signed with SigV4 exactly as AWS defines it. Risk: a canonicalisation slip makes
// every store call 403. Vector: AWS S3 docs, "Example: GET Object" (examplebucket, test.txt, Range bytes=0-9).
func TestSigV4Vector(t *testing.T) {
	sig, signed, _ := sigv4("GET", "/test.txt", nil, map[string]string{
		"Host": "examplebucket.s3.amazonaws.com", "Range": "bytes=0-9",
		"x-amz-content-sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "x-amz-date": "20130524T000000Z",
	}, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "us-east-1", "20130524T000000Z")
	if signed != "host;range;x-amz-content-sha256;x-amz-date" || sig != "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41" {
		t.Fatalf("signed %s sig %s", signed, sig)
	}
}
