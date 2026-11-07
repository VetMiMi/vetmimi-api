package tokens

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// oauthState is who started a connector's sign-in, and until when it may be
// finished.
type oauthState struct {
	User string `json:"u"`
	Exp  int64  `json:"exp"`
}

// SignOAuthState returns the state a connector sends to a platform's sign-in
// dialog, which only user may bring back, before exp. label names the
// platform, so a state from one connector is useless to another.
func SignOAuthState(secret []byte, label, user string, exp time.Time) string {
	payload, _ := json.Marshal(oauthState{User: user, Exp: exp.Unix()}) // two plain fields always marshal
	body := base64.RawURLEncoding.EncodeToString(payload)
	signature := signState(secret, label, body)
	return body + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// CheckOAuthState reports whether signed is a state SignOAuthState signed under
// label for user that has not expired at now.
func CheckOAuthState(secret []byte, label, signed, user string, now time.Time) bool {
	body, signature, _ := strings.Cut(signed, ".")
	got, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || !hmac.Equal(got, signState(secret, label, body)) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return false
	}
	var state oauthState
	err = json.Unmarshal(raw, &state)
	if err != nil {
		return false
	}
	return state.User == user && now.Unix() < state.Exp
}

func signState(secret []byte, label, body string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(label + "-oauth-state."))
	mac.Write([]byte(body))
	return mac.Sum(nil)
}
