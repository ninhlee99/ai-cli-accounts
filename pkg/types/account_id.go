package types

import (
	"fmt"
	"strings"
	"unicode"
)

// EmailLocalPart returns the sanitized lower-case local part of an email
// (chars before @, letters+digits only). "Ninh.Le@x.com" → "ninhle".
func EmailLocalPart(email string) string {
	return sanitizeEmailSegment(emailLocalRaw(email))
}

// EmailDomainPart returns the sanitized domain of an email
// (chars after @, letters+digits only). "a@gmail.com" → "gmailcom".
func EmailDomainPart(email string) string {
	return sanitizeEmailSegment(emailDomainRaw(email))
}

func emailLocalRaw(email string) string {
	email = strings.TrimSpace(strings.ToLower(email))
	if email == "" {
		return ""
	}
	if i := strings.IndexByte(email, '@'); i >= 0 {
		return email[:i]
	}
	return email
}

func emailDomainRaw(email string) string {
	email = strings.TrimSpace(strings.ToLower(email))
	if i := strings.IndexByte(email, '@'); i >= 0 && i+1 < len(email) {
		return email[i+1:]
	}
	return ""
}

func sanitizeEmailSegment(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// AccountNamedID builds a stable pool ID from brand, method, and email:
// "claude", "web", "ninhle@x.com" → "claude:web:ninhle".
// Returns "" when the email has no usable local part.
func AccountNamedID(brand, method, email string) string {
	return accountNamedID(brand, method, email, false)
}

// AccountNamedIDWithDomain disambiguates same local-parts across domains:
// "claude", "web", "ninhle@gmail.com" → "claude:web:ninhle-gmailcom".
func AccountNamedIDWithDomain(brand, method, email string) string {
	return accountNamedID(brand, method, email, true)
}

// AccountBrandID builds brand:local (no method segment) — ChatGPT web:
// "chatgpt", "ninhle@x.com" → "chatgpt:ninhle".
func AccountBrandID(brand, email string) string {
	return accountBrandID(brand, email, false)
}

// AccountBrandIDWithDomain → "chatgpt:ninhle-gmailcom".
func AccountBrandIDWithDomain(brand, email string) string {
	return accountBrandID(brand, email, true)
}

func accountBrandID(brand, email string, withDomain bool) string {
	brand = strings.TrimSpace(strings.ToLower(brand))
	local := EmailLocalPart(email)
	if brand == "" || local == "" {
		return ""
	}
	handle := local
	if withDomain {
		if domain := EmailDomainPart(email); domain != "" {
			handle = local + "-" + domain
		}
	}
	return fmt.Sprintf("%s:%s", brand, handle)
}

func accountNamedID(brand, method, email string, withDomain bool) string {
	brand = strings.TrimSpace(strings.ToLower(brand))
	method = strings.TrimSpace(strings.ToLower(method))
	local := EmailLocalPart(email)
	if brand == "" || method == "" || local == "" {
		return ""
	}
	handle := local
	if withDomain {
		if domain := EmailDomainPart(email); domain != "" {
			handle = local + "-" + domain
		}
	}
	return fmt.Sprintf("%s:%s:%s", brand, method, handle)
}
