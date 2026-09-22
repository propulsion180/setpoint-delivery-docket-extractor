package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

type attachment struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ContentType  string `json:"contentType"`
	ContentBytes string `json:"contentBytes"`
}

type attachmentsRespose struct {
	Value []attachment `json:"value"`
}

func downloadAttachments(accessToken, messageID string) ([]string, error) {
	url := fmt.Sprintf("http://graph.microsoft.com/v1.0/me/messages/%s/attachments", messageID)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("graphreturned %d: %s", resp.StatusCode, string(body))
	}

	var result attachmentsRespose
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if err := os.MkdirAll("attachments", 0755); err != nil {
		return nil, err
	}

	var savedPaths []string
	for _, att := range result.Value {
		if att.ContentType != "application/pdf" {
			log.Printf("skipping non-PDF attachment: %s (%s)\n", att.Name, att.ContentType)
			continue
		}

		data, err := base64.StdEncoding.DecodeString(att.ContentBytes)
		if err != nil {
			log.Printf("failed to decode attachment %s: %v\n", att.Name, err)
			continue
		}

		path := filepath.Join("attachments", messageID+"_"+att.Name)
		if err := os.WriteFile(path, data, 0644); err != nil {
			log.Printf("failed to save attachment %s: %v\n", att.Name, err)
			continue
		}

		log.Printf("saved attachment: %s\n", path)
		savedPaths = append(savedPaths, path)
	}
	return savedPaths, nil
}
