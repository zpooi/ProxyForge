package handlers

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

func pathInt64(r *http.Request, key string) int64 {
	v := chi.URLParam(r, key)
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}
