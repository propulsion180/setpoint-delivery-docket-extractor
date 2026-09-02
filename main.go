package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	//"go/token"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/joho/godotenv"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/microsoft"
)

var oauthConfig *oauth2.Config

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file found, using system environment")
	}

	oauthConfig = &oauth2.Config{
		ClientID:     os.Getenv("OAUTH_CLIENT_ID"),
		ClientSecret: os.Getenv("OAUTH_CLIENT_SECRET"),
		RedirectURL:  os.Getenv("OAUTH_REDIRECT_URL"),
		Scopes:       []string{"Mail.Read", "offline_access"},
		Endpoint:     microsoft.AzureADEndpoint("consumers"),
	}

	http.HandleFunc("/login", handleLogin)
	http.HandleFunc("/callback", handleCallback)

	if token, err := loadToken(); err == nil {
		ts := oauthConfig.TokenSource(context.Background(), token)
		go pollForNewMail(context.Background(), ts)
	}

	log.Println("Serving")
	log.Fatal(http.ListenAndServe(":8080", nil))

}

func saveToken(token *oauth2.Token) error {
	data, err := json.Marshal(token)
	if err != nil {
		return err
	}

	return os.WriteFile("token.json", data, 0600)
}

func loadToken() (*oauth2.Token, error) {
	data, err := os.ReadFile("token.json")
	if err != nil {
		return nil, err
	}
	var token oauth2.Token
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, err
	}

	return &token, nil
}

func saveDeltaLink(link string) error {
	return os.WriteFile("deltalink.txt", []byte(link), 0600)
}

func loadDeltaLink() (string, error) {
	data, err := os.ReadFile("deltalink.txt")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type deltaResponse struct {
	Value     []map[string]interface{} `json:"value"`
	NextLink  string                   `json:"@odata.nextLink"`
	DeltaLink string                   `json:"@odata.deltaLink"`
}

func pollForNewMail(ctx context.Context, tokenSource oauth2.TokenSource) {
	for {
		log.Println("polling")
		if err := checkMail(ctx, tokenSource); err != nil {
			log.Println("poll error:", err)
		}
		time.Sleep(1 * time.Minute)
	}
}

func checkMail(ctx context.Context, tokenSource oauth2.TokenSource) error {
	token, err := tokenSource.Token()
	if err != nil {
		return fmt.Errorf("token refresh failed: %w", err)
	}

	url, err := loadDeltaLink()
	if err != nil {
		//url = "https://graph.microsoft.com/v1.0/me/mailFolders/inbox/messages/delta"
		url = "https://graph.microsoft.com/v1.0/me/mailFolders/inbox/messages/delta?$deltatoken=latest"
	}

	for url != "" {
		log.Println("fetching: ", url)
		req, _ := http.NewRequest("GET", url, nil)
		req.Header.Set("Authorization", "Bearer "+token.AccessToken)
		req.Header.Set("Prefer", "odata.maxpagesize=999")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("graph returned %d: %s", resp.StatusCode, string(body))
		}

		var result deltaResponse
		if err := json.Unmarshal(body, &result); err != nil {
			return err
		}

		for _, msg := range result.Value {
			subject, _ := msg["subject"].(string)
			hasAttachments, _ := msg["hasAttachments"].(bool)
			id, _ := msg["id"].(string)
			if hasAttachments {
				log.Printf("New Message with attachment %s (%s)\n", subject, id)
			}
		}

		if result.NextLink != "" {
			url = result.NextLink
		} else {
			url = ""
			if result.DeltaLink != "" {
				saveDeltaLink(result.DeltaLink)
			}
		}
	}
	return nil
}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	url := oauthConfig.AuthCodeURL("state")
	http.Redirect(w, r, url, http.StatusFound)
}

func handleCallback(w http.ResponseWriter, r *http.Request) {
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		errDesc := r.URL.Query().Get("error_description")
		http.Error(w, fmt.Sprintf("Oauth error: %s - %s", errParam, errDesc), http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "no code in callback", http.StatusBadRequest)
		return
	}

	token, err := oauthConfig.Exchange(context.Background(), code)
	if err != nil {
		http.Error(w, "token exchange failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if err := saveToken(token); err != nil {
		log.Println("failed to save token", err)
	}

	ts := oauthConfig.TokenSource(context.Background(), token)
	go pollForNewMail(context.Background(), ts)

	fmt.Fprintf(w, "Login Sucessfull")

	messages, err := listMessages(token.AccessToken)
	if err != nil {
		fmt.Fprintf(w, "Failed to list mesages: %v", err)
		return
	}
	fmt.Fprintf(w, "Recent messages:\n%s", messages)

}

func listMessages(accessToken string) (string, error) {
	req, err := http.NewRequest("GET", "https://graph.microsoft.com/v1.0/me/messages?$top=5&$select=subject,hasAttachments", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}

	pretty, _ := json.MarshalIndent(result, "", "  ")
	return string(pretty), nil
}
