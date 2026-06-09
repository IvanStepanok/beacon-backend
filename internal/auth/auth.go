// Package auth handles password hashing and analyst JWTs (HS256). The same JWT
// shape is issued by /auth/login and verified by the API middleware; claims carry
// the role + crisis scope that drive RBAC and tenant isolation.
package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/stepanok/beacon-server/internal/model"
)

const TokenTTL = 12 * time.Hour

type Claims struct {
	Email       string   `json:"email"`
	Name        string   `json:"name"`
	Role        string   `json:"role"`
	Region      *string  `json:"region,omitempty"`
	CrisisScope []string `json:"crisisScope"`
	jwt.RegisteredClaims
}

func HashPassword(plain string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(plain), bcrypt.DefaultCost)
	return string(b), err
}

func CheckPassword(hash, plain string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plain)) == nil
}

// Issue mints a signed JWT for a user (issuedAt now, expires at now+TTL).
func Issue(u model.User, secret string, now time.Time) (string, error) {
	claims := Claims{
		Email:       u.Email,
		Name:        u.Name,
		Role:        u.Role,
		Region:      u.Region,
		CrisisScope: u.CrisisScope,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   u.ID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(TokenTTL)),
			Issuer:    "beacon",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// Parse validates a token's signature + expiry and returns the user it represents.
func Parse(token, secret string) (model.User, error) {
	var c Claims
	parsed, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(secret), nil
	})
	if err != nil || !parsed.Valid {
		return model.User{}, errors.New("invalid token")
	}
	return model.User{
		ID:          c.Subject,
		Email:       c.Email,
		Name:        c.Name,
		Role:        c.Role,
		Region:      c.Region,
		CrisisScope: c.CrisisScope,
	}, nil
}
