// Package api carries the published OpenAPI document and serves it. The
// document is the source of truth: handlers and contract tests are validated
// against it, not generated from handler comments.
package api

import _ "embed"

// Spec is the OpenAPI 3.1 document committed at api/openapi.yaml.
//
//go:embed openapi.yaml
var Spec []byte
