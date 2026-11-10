package credentials

import (
	"net/http"
	"time"

	"github.com/MisterDonnuts/thulu/internal/cloudsign"
)

type awsCreds = cloudsign.AWSCreds

func signV4(req *http.Request, body []byte, creds awsCreds, region, service string, now time.Time) error {
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8") //i18n:ignore HTTP header value
	}
	return cloudsign.SignV4(req, body, creds, region, service, now)
}
