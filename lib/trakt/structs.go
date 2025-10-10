package trakt

import (
	"net/http"

	"crovlune/plaxt/lib/common"
	"crovlune/plaxt/lib/store"
)

type Trakt struct {
	ClientId     string
	clientSecret string
	storage      store.Store
	httpClient   *http.Client
	ml           common.MultipleLock
}

type HttpError struct {
	Code    int
	Message string
}
