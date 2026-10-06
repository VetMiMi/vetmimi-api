package video

import (
	"crypto/hmac"
	"crypto/sha1" // coturn defines its REST-API credential as HMAC-SHA1
	"encoding/base64"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// stunServer is Google's public STUN server, enough when neither side is
// behind a strict NAT.
const stunServer = "stun:stun.l.google.com:19302"

// ICEServer is one entry of RTCPeerConnection's iceServers.
type ICEServer struct {
	URLs       []string
	Username   string
	Credential string
}

// ICEServers is Google STUN plus, when turnHost is set, our coturn with a
// time-limited credential in coturn's REST-API convention
// (docs/architecture.md, "Video join", step 4): username
// "<expiry>:<room>:<role>", credential base64(HMAC-SHA1(secret, username)),
// expiring when the room closes.
func ICEServers(turnHost, secret string, room pgtype.UUID, role string, closesAt time.Time) []ICEServer {
	servers := []ICEServer{{URLs: []string{stunServer}}}
	if turnHost == "" || secret == "" {
		return servers
	}
	username := fmt.Sprintf("%d:%s:%s", closesAt.Unix(), room.String(), role)
	return append(servers, ICEServer{
		URLs:       []string{"turn:" + turnHost + "?transport=udp", "turn:" + turnHost + "?transport=tcp"},
		Username:   username,
		Credential: TURNCredential(secret, username),
	})
}

// TURNCredential is coturn's password for username under its
// static-auth-secret.
func TURNCredential(secret, username string) string {
	mac := hmac.New(sha1.New, []byte(secret))
	mac.Write([]byte(username))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}
