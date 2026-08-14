package apiapp

import (
	"errors"
	"net/http"
)

func asMaxBytes(err error, target **http.MaxBytesError) bool {
	return errors.As(err, target)
}
