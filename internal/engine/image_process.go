package engine

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	_ "image/gif"
)

const (
	inlineImageMaxDimension = 2000
	inlineImageMaxBase64    = int(4.5 * 1024 * 1024)
)

type processedImage struct {
	Data, MIME, Hint              string
	OriginalWidth, OriginalHeight int
	Width, Height                 int
}

func processInlineImage(input []byte, inputMIME string) (processedImage, error) {
	decoded, _, err := image.Decode(bytes.NewReader(input))
	if err != nil {
		return processedImage{}, fmt.Errorf("image omitted: could not decode image: %w", err)
	}
	bounds := decoded.Bounds()
	originalWidth, originalHeight := bounds.Dx(), bounds.Dy()
	encodedSize := base64.StdEncoding.EncodedLen(len(input))
	supportedOriginal := inputMIME == "image/png" || inputMIME == "image/jpeg" || inputMIME == "image/gif" || inputMIME == "image/webp"
	if supportedOriginal && originalWidth <= inlineImageMaxDimension && originalHeight <= inlineImageMaxDimension && encodedSize < inlineImageMaxBase64 {
		return processedImage{Data: base64.StdEncoding.EncodeToString(input), MIME: inputMIME, OriginalWidth: originalWidth, OriginalHeight: originalHeight, Width: originalWidth, Height: originalHeight}, nil
	}
	width, height := fitImageDimensions(originalWidth, originalHeight, inlineImageMaxDimension, inlineImageMaxDimension)
	for {
		destination := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.CatmullRom.Scale(destination, destination.Bounds(), decoded, bounds, draw.Over, nil)
		candidates := []struct {
			data []byte
			mime string
		}{}
		var pngBuffer bytes.Buffer
		if png.Encode(&pngBuffer, destination) == nil {
			candidates = append(candidates, struct {
				data []byte
				mime string
			}{pngBuffer.Bytes(), "image/png"})
		}
		for _, quality := range []int{80, 85, 70, 55, 40} {
			var jpegBuffer bytes.Buffer
			if jpeg.Encode(&jpegBuffer, destination, &jpeg.Options{Quality: quality}) == nil {
				candidates = append(candidates, struct {
					data []byte
					mime string
				}{jpegBuffer.Bytes(), "image/jpeg"})
			}
		}
		for _, candidate := range candidates {
			if base64.StdEncoding.EncodedLen(len(candidate.data)) < inlineImageMaxBase64 {
				hint := ""
				if width != originalWidth || height != originalHeight {
					scale := float64(originalWidth) / float64(width)
					hint = fmt.Sprintf("[Image: original %dx%d, displayed at %dx%d. Multiply coordinates by %.2f to map to original image.]", originalWidth, originalHeight, width, height, scale)
				} else if inputMIME != candidate.mime {
					hint = fmt.Sprintf("[Image converted from %s to %s.]", inputMIME, candidate.mime)
				}
				return processedImage{Data: base64.StdEncoding.EncodeToString(candidate.data), MIME: candidate.mime, Hint: hint, OriginalWidth: originalWidth, OriginalHeight: originalHeight, Width: width, Height: height}, nil
			}
		}
		if width == 1 && height == 1 {
			break
		}
		width, height = max(1, int(math.Floor(float64(width)*0.75))), max(1, int(math.Floor(float64(height)*0.75)))
	}
	return processedImage{}, fmt.Errorf("image omitted: could not resize below inline image size limit")
}

func fitImageDimensions(width, height, maxWidth, maxHeight int) (int, int) {
	if width > maxWidth {
		height = int(math.Round(float64(height) * float64(maxWidth) / float64(width)))
		width = maxWidth
	}
	if height > maxHeight {
		width = int(math.Round(float64(width) * float64(maxHeight) / float64(height)))
		height = maxHeight
	}
	return max(1, width), max(1, height)
}
