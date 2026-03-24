package fieldfilter

import "encoding/json"

// UnsafeFieldPatterns are auto-denied to prevent leaking sensitive data.
var UnsafeFieldPatterns = []string{
	"password", "secret", "token", "key", "price", "credential", "private",
}

// FilterFields strips denied fields and restricts to allowed fields.
// Applies UnsafeFieldPatterns auto-filter regardless of explicit lists.
func FilterFields(data json.RawMessage, allowed, denied []string) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return data
	}

	deniedSet := make(map[string]bool, len(denied))
	for _, d := range denied {
		deniedSet[toLower(d)] = true
	}

	allowedSet := make(map[string]bool, len(allowed))
	for _, a := range allowed {
		allowedSet[toLower(a)] = true
	}

	filtered := make(map[string]json.RawMessage)
	for k, v := range obj {
		kLower := toLower(k)
		if deniedSet[kLower] {
			continue
		}
		if MatchesUnsafePattern(kLower) {
			continue
		}
		if len(allowedSet) > 0 && !allowedSet[kLower] {
			continue
		}
		filtered[k] = v
	}

	result, err := json.Marshal(filtered)
	if err != nil {
		return data
	}
	return result
}

// MatchesUnsafePattern checks if a lowercase field name contains any unsafe pattern.
func MatchesUnsafePattern(fieldLower string) bool {
	for _, pattern := range UnsafeFieldPatterns {
		if containsLower(fieldLower, pattern) {
			return true
		}
	}
	return false
}

func toLower(s string) string {
	b := make([]byte, len(s))
	for i := range s {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func containsLower(s, substr string) bool {
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
