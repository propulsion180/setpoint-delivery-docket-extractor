package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	gofitz "github.com/gen2brain/go-fitz"
)

func pdfToImages(pdfPath string) ([]string, error) {
	doc, err := gofitz.New(pdfPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open pdf: %w", err)
	}
	defer doc.Close()

	if err := os.Mkdir("images", 0755); err != nil {
		return nil, err
	}

	var imagePaths []string
	base := strings.TrimSuffix(filepath.Base(pdfPath), filepath.Ext(pdfPath))

	for pageNum := 0; pageNum < doc.NumPage(); pageNum++ {
		img, err := doc.Image(pageNum)
		if err != nil {
			return nil, fmt.Errorf("failed to render page %d: %w", pageNum, err)
		}

		imgPath := filepath.Join("images", fmt.Sprintf("%s_page%d.png", base, pageNum))
		f, err := os.Create(imgPath)
		if err != nil {
			return nil, err
		}

		if err := png.Encode(f, img); err != nil {
			f.Close()
			return nil, err
		}
		f.Close()

		imagePaths = append(imagePaths, imgPath)
	}

	return imagePaths, nil
}
