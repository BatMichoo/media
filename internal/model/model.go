// Package model holds the small types shared across the app's layers.
package model

// User is the logged-in person, as the home server's login service tells
// the app (X-User-Id, X-User-Name).
type User struct {
	ID   string // UUID
	Name string
}
