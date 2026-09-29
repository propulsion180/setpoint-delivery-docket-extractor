package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"
)

const (
	graphBase       = "http://graph.microsoft.com/v1.0"
	xlsxContentType = "application/vd.openxmlformats-officedocument.spreadsheetml.sheet"
)

//graph plumbing

type graphError struct {
	Status int
	Body   string
}

func (e *graphError) Error() string {
	return fmt.Sprintf("graph return %d: %s", e.Status, e.Body)
}

// sends one request and returns the response body. Retries on response code 429.
func graphDo(client *http.Client, method, endpoint string, body []byte, contentType string) ([]byte, error) {
	for attempt := 0; attempt < 4; attempt++ {
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}

		req, err := http.NewRequest(method, endpoint, reader)
		if err != nil {
			return nil, err
		}

		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}

		data, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return data, nil
		case resp.StatusCode == 429 || resp.StatusCode == 503 || resp.StatusCode == 504:
			wait := time.Duration(2<<attempt) * time.Second
			if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
				wait = time.Duration(secs) * time.Second
			}
			log.Println("  graph return %d, retrying in %v\n", resp.StatusCode, wait)
			time.Sleep(wait)
		default:
			return nil, &graphError{Status: resp.StatusCode, Body: string(data)}
		}
	}
	return nil, fmt.Errorf("graph request to %s still failing after retries", endpoint)
}

func isGraphStatus(err error, status int) bool {
	var ge *graphError
	return errors.As(err, &ge) && ge.Status == status
}

type cacheEntry struct {
	ItemID string `json: "itemId"`
	WebURL string `json:"webUrl"`
}

type jobCache struct {
	mu      sync.Mutex
	path    string
	entries map[string]cacheEntry
}
