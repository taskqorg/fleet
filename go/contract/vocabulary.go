package contract

import "encoding/json"

// Vocabulary: contracts/vocabulary.json, the words the app and the coordinator share (harnesses, view grammar, system
// presets). The app imports the same file; go generate copies it into vocabulary_gen.go.
type VocabularyData struct {
	Causes []struct {
		ID, Text string
		Match    []string
	}
	Harnesses []struct {
		ID, Model, Label, Icon, Login string
		LoginArgv                     []string
		Assets                        bool
	}
	Views []string
	Lists map[string]struct {
		Keys, States, Groups, Sorts []string
		Group, Sort                 string
	}
	SystemViews map[string][]struct {
		Name, Label     string
		State, NotState []string
	}
	NavDefaults map[string]string
}

var Vocabulary = func() (v VocabularyData) {
	if err := json.Unmarshal([]byte(vocabularyJSON), &v); err != nil {
		panic("contract: contracts/vocabulary.json: " + err.Error())
	}
	return
}()

// HarnessIDs: every harness id in vocabulary order.
func HarnessIDs() []string {
	out := []string{}
	for _, h := range Vocabulary.Harnesses {
		out = append(out, h.ID)
	}
	return out
}

// HarnessDefaultModel: the model a start without a profile model records for this harness (its vocabulary row's model);
// no model, or an unknown or empty harness: codex's.
func HarnessDefaultModel(harness string) string {
	fallback := ""
	for _, h := range Vocabulary.Harnesses {
		if h.ID == harness && h.Model != "" {
			return h.Model
		}
		if h.ID == "codex" {
			fallback = h.Model
		}
	}
	return fallback
}

// PlainCause: a stored failure cause code (an execution's problem, such as unknown:abandoned or step:provider-429) in
// plain words: the vocabulary's causes entry whose match the code starts with first. Bots, Home and Live read the same
// sentence; the code goes under Details. Empty code: nil.
func PlainCause(code, detail string) *Failure {
	if code == "" {
		return nil
	}
	for _, c := range Vocabulary.Causes {
		for _, m := range c.Match {
			if len(code) >= len(m) && code[:len(m)] == m {
				f := &Failure{Cause: c.ID, Code: code, Text: c.Text}
				if detail != "" && detail != code {
					f.Detail = &detail
				}
				return f
			}
		}
	}
	panic("contract: vocabulary.json causes: the last entry must match every code")
}
