package main

import (
	"errors"
)

func mcpResultText(result M) (string, error) {
	if jAt(result, "isError") == true {
		return "", errors.New("MCP reported tool failure")
	}
	text := ""
	for _, c := range jArr(result, "content") {
		if jStr(c, "type") == "text" {
			text += jStr(c, "text")
		}
	}
	if text == "" {
		return "", errors.New("MCP returned no textual witness")
	}
	return text, nil
}

func serenaSymbolWitness(text string) error {
	value, err := dec(text)
	if err != nil {
		return errors.New("Serena symbol result is not structured navigation evidence (check language-server readiness)")
	}
	for _, names := range jObj(value) {
		for _, name := range jArr(names) {
			if jStr(name) == "CapabilityWitness" {
				return nil
			}
		}
	}
	return errors.New("Serena did not observe the known fixture symbol")
}
