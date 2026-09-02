package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

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

	log.Fatal(http.ListenAndServe(":8080", nil))

}

func handleLogin(w http.ResponseWriter, r *http.Request) {
	url := oauthConfig.AuthCodeURL("state")
	http.Redirect(w, r, url, http.StatusFound)
}

func handleCallback(w http.ResponseWriter, r *http.Request) {
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
