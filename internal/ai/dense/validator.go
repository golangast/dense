package dense

import (
	"go/types"
	"strings"
)

// IsExternalImportError checks if a type-checker error is safe to ignore during local AST edits.
func IsExternalImportError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "could not import") ||
		strings.Contains(msg, "cannot find package") ||
		strings.Contains(msg, "no required module provides package") ||
		strings.Contains(msg, "can't find import") ||
		strings.Contains(msg, "imported and not used")
}

// ValidateASTWithTolerances rejects genuine syntax/type mistakes while tolerating missing external imports.
func ValidateASTWithTolerances(pkg *types.Package, errs []error) error {
	for _, err := range errs {
		if err == nil {
			continue
		}
		if !IsExternalImportError(err) {
			return err
		}
	}
	return nil
}
