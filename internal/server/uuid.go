// SCOPE:layer=infra,removal=core — UUID generator for per-process identifiers
//
// Extracted into its own file so the crdtstore transport wiring can
// generate a PublisherID at startup, and tests can override the
// generator if they need deterministic IDs.
package server

import "github.com/google/uuid"

// newUUID is the default UUID generator. Wrapped in a function
// variable so tests can replace it.
var newUUID = uuid.NewString
