package postgres

import (
	"encoding/json"

	"github.com/uniandes-isis4426/okfp/internal/domain"
)

func marshalReport(r *domain.ValidationReport) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return json.Marshal(r)
}
