package main

import (
	"fmt"

	"github.com/otiai10/gosseract/v2"
)

func ocrImage(imagePath string) (string, error) {
	client := gosseract.NewClient()
	defer client.Close()

	if err := client.SetImage(imagePath); err != nil {
		return "", fmt.Errorf("failed to set gosseract image: %w", err)
	}

	text, err := client.Text()
	if err != nil {
		return "", fmt.Errorf("failed to set image: %w", err)
	}

	return text, nil
}
