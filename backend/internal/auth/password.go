package auth

import "golang.org/x/crypto/bcrypt"

// bcryptCost is fixed at 10 per PLAN.md risk #3. Higher cost blows the
// signup latency budget; lower cost makes the hashes trivially brute-forceable.
// TestPasswordCostLocked in auth_test.go pins this value.
const bcryptCost = 10

// HashPassword returns a bcrypt hash of plaintext using the locked cost.
func HashPassword(plaintext string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// VerifyPassword reports nil if plaintext matches the stored hash, and an
// error otherwise. Callers should map that to ErrInvalidCredentials so they
// never expose bcrypt's own error (which can hint at the failure mode).
func VerifyPassword(hash, plaintext string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext))
}

// CostOf returns the bcrypt cost embedded in hash. Used by the cost-lock test.
func CostOf(hash string) (int, error) {
	return bcrypt.Cost([]byte(hash))
}
