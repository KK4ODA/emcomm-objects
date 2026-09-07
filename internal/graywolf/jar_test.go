package graywolf

import (
	"net/http"
	"net/http/cookiejar"
)

func newJar() (http.CookieJar, error) { return cookiejar.New(nil) }
