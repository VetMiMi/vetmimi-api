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
	var parsed message
	decoder := json.NewDecoder(bytes.NewReader(frame))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&parsed) != nil || decoder.More() {
		return ""
	}
	noPayload := parsed.SDP == nil && parsed.Candidate == nil
	switch {
	case (parsed.Type == "join" || parsed.Type == "leave") && noPayload,
		(parsed.Type == "offer" || parsed.Type == "answer") && parsed.SDP != nil && parsed.Candidate == nil,
		parsed.Type == "ice" && parsed.SDP == nil && parsed.Candidate != nil && parsed.Candidate.Candidate != nil:
		return parsed.Type
	}
	return ""
}
