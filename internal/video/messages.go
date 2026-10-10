package video

import (
	"bytes"
	"encoding/json"
)

// message is a frame from a participant. Only its shape is checked; the
// original bytes are relayed.
type message struct {
	Type      string     `json:"type"`
	SDP       *string    `json:"sdp"`
	Candidate *candidate `json:"candidate"`
}

type candidate struct {
	Candidate     *string `json:"candidate"`
	SDPMid        *string `json:"sdpMid"`
	SDPMLineIndex *int    `json:"sdpMLineIndex"`
}

// peerState is the hub's one message to participants.
type peerState struct {
	Type         string `json:"type"`
	Client       string `json:"client"`
	Practitioner string `json:"practitioner"`
	Room         string `json:"room"`
}

// messageType returns the type of a frame that fits a message shape exactly, or "".
func messageType(frame []byte) string {
	var m message
	dec := json.NewDecoder(bytes.NewReader(frame))
	dec.DisallowUnknownFields()
	if dec.Decode(&m) != nil || dec.More() {
		return ""
	}
	noPayload := m.SDP == nil && m.Candidate == nil
	switch {
	case (m.Type == "join" || m.Type == "leave") && noPayload,
		(m.Type == "offer" || m.Type == "answer") && m.SDP != nil && m.Candidate == nil,
		m.Type == "ice" && m.SDP == nil && m.Candidate != nil && m.Candidate.Candidate != nil:
		return m.Type
	}
	return ""
}
