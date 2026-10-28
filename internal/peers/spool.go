//go:build unix

package peers

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// maxMessageBytes bounds a spooled message so a bad file cannot exhaust memory.
const maxMessageBytes = 1 << 20

// Message is one spooled request or reply. Content is untrusted model text.
type Message struct {
	ID           string    `json:"id"`
	From         string    `json:"from"`
	FromLabel    string    `json:"from_label"`
	To           string    `json:"to"`
	Content      string    `json:"content"`
	Hop          int       `json:"hop"`
	ExpectsReply bool      `json:"expects_reply"`
	InReplyTo    string    `json:"in_reply_to,omitempty"`
	Status       string    `json:"status,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
	MAC          string    `json:"mac,omitempty"`
}

// NewMessageID returns a time-ordered, unique message id.
func NewMessageID() string {
	random := make([]byte, 4)
	_, _ = rand.Read(random)
	return fmt.Sprintf("%d-%s", time.Now().UTC().UnixNano(), hex.EncodeToString(random))
}

func (r *RunDir) inboxDir(endpoint string) string {
	return filepath.Join(r.path, "inbox", endpoint)
}

func (r *RunDir) sign(message Message) (string, error) {
	message.MAC = ""
	data, err := json.Marshal(message)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, r.key)
	_, _ = mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func (r *RunDir) verify(message Message) bool {
	want, err := r.sign(message)
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(want), []byte(message.MAC))
}

// Spool signs message and atomically places it in its recipient's inbox.
func (r *RunDir) Spool(message Message) error {
	if err := validEndpoint(message.To); err != nil {
		return err
	}
	if err := validMessageID(message.ID); err != nil {
		return err
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}
	mac, err := r.sign(message)
	if err != nil {
		return err
	}
	message.MAC = mac
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(data) > maxMessageBytes {
		return fmt.Errorf("peer message is larger than %d bytes", maxMessageBytes)
	}
	dir := r.inboxDir(message.To)
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, message.ID+".json"))
}

// Pending returns the verified messages waiting for endpoint, oldest first.
// Files that fail verification are renamed aside and reported through
// rejected so they are not retried.
func (r *RunDir) Pending(endpoint string) (messages []Message, rejected []error, err error) {
	dir := r.inboxDir(endpoint)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, name)
		message, problem := r.readMessage(path, endpoint)
		if problem != nil {
			rejected = append(rejected, fmt.Errorf("%s: %w", name, problem))
			_ = os.Rename(path, path+".rejected")
			continue
		}
		messages = append(messages, message)
	}
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].CreatedAt.Before(messages[j].CreatedAt) })
	return messages, rejected, nil
}

func (r *RunDir) readMessage(path, endpoint string) (Message, error) {
	if err := checkPrivate(path, false); err != nil {
		return Message{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return Message{}, err
	}
	if info.Size() > maxMessageBytes {
		return Message{}, fmt.Errorf("message is too large")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Message{}, err
	}
	var message Message
	if err := json.Unmarshal(data, &message); err != nil {
		return Message{}, fmt.Errorf("malformed message: %w", err)
	}
	if !r.verify(message) {
		return Message{}, fmt.Errorf("invalid or missing signature")
	}
	if message.To != endpoint || strings.TrimSuffix(filepath.Base(path), ".json") != message.ID {
		return Message{}, fmt.Errorf("message is addressed to %q, not %q", message.To, endpoint)
	}
	return message, nil
}

// Ack removes a processed message from endpoint's inbox.
func (r *RunDir) Ack(endpoint, id string) error {
	if err := validMessageID(id); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(r.inboxDir(endpoint), id+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// PendingCount returns how many messages wait in endpoint's inbox.
func (r *RunDir) PendingCount(endpoint string) int {
	entries, err := os.ReadDir(r.inboxDir(endpoint))
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count
}

func validMessageID(id string) error {
	if id == "" || strings.ContainsAny(id, `/\`) || strings.HasPrefix(id, ".") {
		return fmt.Errorf("invalid peer message id %q", id)
	}
	return nil
}
