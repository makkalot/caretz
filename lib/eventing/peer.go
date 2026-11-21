package eventing

import (
	"encoding/json"
	"fmt"

	"github.com/makkalot/caretz/lib/types"
)

const (
	// PeerMessageSent records a SendMessage call in the sender's log.
	PeerMessageSent = "Peer.MessageSent"
	// PeerMessageReceived records an inbound peer message or reply in the
	// receiver's log before its turn is requested.
	PeerMessageReceived = "Peer.MessageReceived"
	// PeerReplyForwarded records that the result of an inbound message was
	// spooled back to its sender.
	PeerReplyForwarded = "Peer.ReplyForwarded"

	AsyncRunPeer = "peer"
)

type PeerMessageSentPayload struct {
	MessageID string `json:"message_id"`
	To        string `json:"to"`
	Hop       int    `json:"hop"`
}

type PeerMessageReceivedPayload struct {
	MessageID    string `json:"message_id"`
	From         string `json:"from"`
	FromLabel    string `json:"from_label"`
	Content      string `json:"content"`
	Hop          int    `json:"hop"`
	ExpectsReply bool   `json:"expects_reply"`
	InReplyTo    string `json:"in_reply_to,omitempty"`
	Status       string `json:"status,omitempty"`
}

type PeerReplyForwardedPayload struct {
	MessageID string `json:"message_id"`
	ReplyID   string `json:"reply_id"`
}

func NewPeerMessageSent(originatorID string, payload PeerMessageSentPayload) *types.Event {
	return newPeerEvent(originatorID, PeerMessageSent, payload)
}

func NewPeerMessageReceived(originatorID string, payload PeerMessageReceivedPayload) *types.Event {
	return newPeerEvent(originatorID, PeerMessageReceived, payload)
}

func NewPeerReplyForwarded(originatorID string, payload PeerReplyForwardedPayload) *types.Event {
	return newPeerEvent(originatorID, PeerReplyForwarded, payload)
}

// DecodePeerEvent decodes the payload of any Peer.* event into target.
func DecodePeerEvent(event *types.Event, target any) error {
	if event == nil {
		return fmt.Errorf("peer event is nil")
	}
	return json.Unmarshal([]byte(event.Payload), target)
}

func newPeerEvent(originatorID, eventType string, payload any) *types.Event {
	data, _ := json.Marshal(payload)
	return types.NewEvent(types.NewOriginator(originatorID, 0), eventType, string(data))
}
