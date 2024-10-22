// Package pagemark defines the page marker the converters insert at the start of each page.
package pagemark

import "fmt"

// Marker returns the line inserted at the start of page n (1-based).
func Marker(n int) string {
	return fmt.Sprintf("<!-- page %d -->", n)
}
