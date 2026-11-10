package main

// Placeholders until part E1's helpers land (profile.rs, harness.rs placement and native id). E1's merge deletes this file.

import "errors"

func profileValidate(any) error { return errors.New("profile validation not ported") }

func validateAntigravitySettings(any) error { return nil }

func safeNativeID(n string) bool {
	if n == "" || len(n) > 64 || n[0] == '-' {
		return false
	}
	for _, c := range n {
		if !(c == '-' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return true
}

func claudePlacementHere() (string, error) { return "", errors.New("claude-placement-unset") }
