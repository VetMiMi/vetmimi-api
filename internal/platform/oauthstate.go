package platform

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// oauthState is what a platform connector's OAuth state carries: who
// started the sign-in, and until when it may be finished.
type oauthState struct {
	User string `json:"u"`
	Exp  int64  `json:"exp"`
}

// SignOAuthState returns the state a connector sends to a platform's
// sign-in dialog, which only user may bring back, before exp. label names
// the platform, so a state from one connector is useless to another.
func SignOAuthState(secret []byte, label, user string, exp time.Time) string {
	payload, _ := json.Marshal(oauthState{User: user, Exp: exp.Unix()}) // two plain fields always marshal
	body := base64.RawURLEncoding.EncodeToString(payload)
	return body + "." + base64.RawURLEncoding.EncodeToString(signState(secret, label, body))
}

// CheckOAuthState reports whether s is a state SignOAuthState signed under
// label for user that has not expired at now. The signature is compared
// before anything in the state is trusted.
func CheckOAuthState(secret []byte, label, s, user string, now time.Time) bool {
	body, sig, _ := strings.Cut(s, ".")
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, signState(secret, label, body)) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	var st oauthState
	return err == nil && json.Unmarshal(raw, &st) == nil && st.User == user && now.Unix() < st.Exp
}

func signState(secret []byte, label, body string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(label + "-oauth-state."))
	mac.Write([]byte(body))
	return mac.Sum(nil)
}
