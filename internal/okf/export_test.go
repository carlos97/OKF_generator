package okf_test

import (
	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/okf"
	"github.com/uniandes-isis4426/okfp/internal/okf/validate"
)

// validateAgain re-evalua el bundle tras manipularlo en un test.
func validateAgain(out *okf.Output) *domain.ValidationReport {
	return validate.Run(out.FS, nil)
}
