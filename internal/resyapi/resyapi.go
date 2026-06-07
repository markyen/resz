package resyapi

import "net/http"

const (
	APIKey    = `ResyAPI api_key="AIcdK2rLXG6TYwJseSbmrBAy3RP81ocd"`
	UserAgent = "Resy/3.41.1 (com.resy.ResyApp; build:8073; iOS 26.4.2) Alamofire/5.11.1"
	BaseURL   = "https://api.resy.com"
)

// setResyHeaders applies the standard Resy request headers shared by every
// endpoint. If authToken is non-empty, the authenticated
// X-Resy-Universal-Auth header is added. Callers that need a Content-Type
// set it themselves after calling this.
func setResyHeaders(req *http.Request, authToken string) {
	req.Header.Set("Authorization", APIKey)
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", UserAgent)
	if authToken != "" {
		req.Header.Set("X-Resy-Universal-Auth", authToken)
	}
}
