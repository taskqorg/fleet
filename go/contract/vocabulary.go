package contract

import "encoding/json"

// Vocabulary: contracts/vocabulary.json, the words the app and the coordinator share (harnesses, view grammar, system
// presets). The app imports the same file; go generate copies it into vocabulary_gen.go.
type VocabularyData struct {
	Harnesses []struct {
		ID, Label, Icon, Login string
		LoginArgv              []string
		Assets                 bool
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
