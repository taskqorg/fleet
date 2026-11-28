package contract

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// PreviewRecipe is the input to the book and normal-app recipe digest.
type PreviewRecipe struct {
	Version       int                  `json:"version"`
	Scene         string               `json:"scene"`
	State         string               `json:"state"`
	Path          string               `json:"path"`
	Landmark      string               `json:"landmark"`
	Clock         int64                `json:"clock"`
	TimeZone      string               `json:"timeZone"`
	Locale        string               `json:"locale"`
	DPR           int                  `json:"dpr"`
	ReducedMotion bool                 `json:"reducedMotion"`
	Matrix        []PreviewRecipeCell  `json:"matrix"`
	CSS           string               `json:"css"`
	Inputs        []PreviewRecipeInput `json:"inputs"`
}

type PreviewRecipeCell struct {
	Viewport string `json:"viewport"`
	Theme    string `json:"theme"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type PreviewRecipeInput struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func (r *PreviewRecipe) Digest() string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(r) // only strings, integers and a boolean
	raw := bytes.TrimSuffix(b.Bytes(), []byte{'\n'})
	sum := sha256.Sum256(append([]byte("fleet:preview-recipe:v1\x00"), raw...))
	return hex.EncodeToString(sum[:])
}
