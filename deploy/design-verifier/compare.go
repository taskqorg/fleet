// This guest sees PNGs only. It never receives candidate code or protected credentials.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
)

type comparison struct {
	Width, Height    int
	Original, Actual []byte
}

type bounds struct {
	X, Y, Width, Height int
}

type difference struct {
	DifferingPixels int     `json:"differingPixels"`
	Bounds          *bounds `json:"bounds,omitempty"`
}

func decode(b []byte, width, height int) (image.Image, error) {
	cfg, err := png.DecodeConfig(bytes.NewReader(b))
	if err != nil || cfg.Width != width || cfg.Height != height {
		return nil, errors.New("wrong dimensions or PNG format")
	}
	return png.Decode(bytes.NewReader(b))
}

func compare(input comparison) (difference, error) {
	var result difference
	if input.Width != 1440 || input.Height != 900 {
		if input.Width != 390 || input.Height != 844 {
			return result, errors.New("unsupported viewport")
		}
	}
	a, err := decode(input.Original, input.Width, input.Height)
	if err != nil {
		return result, err
	}
	b, err := decode(input.Actual, input.Width, input.Height)
	if err != nil {
		return result, err
	}
	minX, minY, maxX, maxY := input.Width, input.Height, 0, 0
	for y := 0; y < input.Height; y++ {
		for x := 0; x < input.Width; x++ {
			if color.NRGBA64Model.Convert(a.At(x, y)) != color.NRGBA64Model.Convert(b.At(x, y)) {
				result.DifferingPixels++
				minX, minY = min(minX, x), min(minY, y)
				maxX, maxY = max(maxX, x), max(maxY, y)
			}
		}
	}
	if result.DifferingPixels > 0 {
		result.Bounds = &bounds{minX, minY, maxX - minX + 1, maxY - minY + 1}
	}
	return result, nil
}

func main() {
	var input comparison
	d := json.NewDecoder(io.LimitReader(os.Stdin, 48<<20))
	if d.Decode(&input) != nil || d.Decode(new(any)) != io.EOF {
		os.Exit(1)
	}
	changed, err := compare(input)
	if err != nil {
		os.Exit(1)
	}
	if json.NewEncoder(os.Stdout).Encode(changed) != nil {
		os.Exit(1)
	}
}
