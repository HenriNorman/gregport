// Package customers implements org-scoped customer data erasure.
//
// Erasure keeps ticket history intact: tickets, comments and events continue
// to reference the customer row, but the identifying fields are replaced. The
// email becomes a deterministic SHA-256 pseudonym derived from the org and
// customer ids, so repeated erasure calls converge on the same value and the
// per-org uniqueness constraint is preserved.
package customers

import (
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

// ErrCustomerNotFound is returned when a customer is absent from the caller's
// org. Customers in another org are indistinguishable from missing ones.
var ErrCustomerNotFound = httpx.NotFound("customer_not_found", "customer not found")

// PseudonymDomain separates these addresses from real mailboxes.
const PseudonymDomain = "anonymized.invalid"

// Customer is the identifying part of a customers row.
type Customer struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	Email      string
	Name       *string
	ExternalID *string
	DeletedAt  *time.Time
}

// AnonymizedEmail derives the stable, org-scoped pseudonym for a customer.
// Hashing the ids rather than the original address makes erasure idempotent:
// the same customer always maps to the same value even after the first call.
func AnonymizedEmail(orgID, customerID uuid.UUID) string {
	sum := sha256.Sum256([]byte(orgID.String() + ":" + customerID.String()))
	return "erased-" + hex.EncodeToString(sum[:]) + "@" + PseudonymDomain
}
