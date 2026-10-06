package video

import (
	"bytes"
	"encoding/json"
)

// message is a frame from a participant, as openapi.yaml's
// connectVideoRoom x-websocket-messages.clientToServer describes it. Only
// its shape is checked; the relay forwards the original bytes.
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

// messageType is the type of a frame that fits one of the shapes exactly,
// or "" for anything else: unknown types, missing or extra fields,
// trailing data.
func messageType(frame []byte) string {
	var m message
	dec := json.NewDecoder(bytes.NewReader(frame))
	dec.DisallowUnknownFields()
	if dec.Decode(&m) != nil || dec.More() {
		return ""
	}
	bare := m.SDP == nil && m.Candidate == nil
	switch {
	case (m.Type == "join" || m.Type == "leave") && bare,
		(m.Type == "offer" || m.Type == "answer") && m.SDP != nil && m.Candidate == nil,
		m.Type == "ice" && m.SDP == nil && m.Candidate != nil && m.Candidate.Candidate != nil:
		return m.Type
	}
	return ""
}

// peerState tells both participants who is connected and how the room
// stands.
type peerState struct {
	Type         string `json:"type"`
	Client       string `json:"client"`
	Practitioner string `json:"practitioner"`
	Room         string `json:"room"`
}
