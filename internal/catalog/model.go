package catalog

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const MaxBodyBytes = 128 * 1024

var (
	ErrNotFound = errors.New("item not found")
	ErrConflict = errors.New("concurrent update; retry the request")
)

// Data preserves the submitted option or exported preset, including unknown
// preset settings and numeric precision. IDs belong to this shared catalog.
type Item struct {
	ID        string          `json:"id" dynamodbav:"sk"`
	Kind      string          `json:"kind" dynamodbav:"pk"`
	CreatedBy string          `json:"createdBy" dynamodbav:"createdBy"`
	CreatedAt string          `json:"createdAt" dynamodbav:"createdAt"`
	Upvotes   int64           `json:"upvotes" dynamodbav:"upvotes"`
	Downvotes int64           `json:"downvotes" dynamodbav:"downvotes"`
	Score     int64           `json:"score" dynamodbav:"score"`
	Data      json.RawMessage `json:"data" dynamodbav:"-"`
}

type Page struct {
	Items      []Item `json:"items"`
	NextCursor string `json:"nextCursor,omitempty"`
}

type Store interface {
	Create(context.Context, Item) error
	Get(context.Context, string, string) (Item, error)
	List(context.Context, string, int, string) (Page, error)
	Vote(context.Context, string, string, string, int) (Item, error)
}

type Option struct {
	Comment          string            `json:"comment"`
	Description      string            `json:"description"`
	Category         string            `json:"category"`
	Type             string            `json:"type"`
	Value            string            `json:"value"`
	Address          *string           `json:"address"`
	GameInit         bool              `json:"gameInit"`
	StatEdit         bool              `json:"statEdit"`
	RawJSON          bool              `json:"rawJson"`
	AdditionalWrites []json.RawMessage `json:"additionalWrites"`
}

func DecodeStrict(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("expected exactly one JSON value")
	}
	return nil
}

func ValidID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil && id == strings.ToLower(id)
}

func NewItem(kind, user string, data []byte) (Item, error) {
	var normalized []byte
	var err error
	switch kind {
	case "options":
		normalized, err = validateOption(data)
	case "presets":
		normalized, err = validatePreset(data)
	default:
		err = errors.New("invalid item kind")
	}
	if err != nil {
		return Item{}, err
	}
	if len(normalized) > MaxBodyBytes {
		return Item{}, errors.New("normalized JSON exceeds 128 KiB")
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return Item{}, err
	}
	return Item{ID: hex.EncodeToString(id), Kind: kind, CreatedBy: user,
		CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), Data: normalized}, nil
}

func object(data []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
		return nil, errors.New("expected a JSON object")
	}
	return obj, nil
}

func validateOption(data []byte) ([]byte, error) {
	if _, err := object(data); err != nil {
		return nil, err
	}
	var o Option
	if err := DecodeStrict(data, &o); err != nil {
		return nil, fmt.Errorf("invalid option: %w", err)
	}
	if strings.TrimSpace(o.Comment) == "" || len(o.Comment) > 200 {
		return nil, errors.New("comment must contain 1–200 bytes")
	}
	if len(o.Description) > 10000 {
		return nil, errors.New("description exceeds 10000 bytes")
	}
	switch o.Category {
	case "world", "items", "challenge", "relics", "gameplay":
	default:
		return nil, errors.New("invalid option category")
	}
	switch o.Type {
	case "char", "short", "word", "long", "string":
	default:
		return nil, errors.New("invalid write type")
	}
	if strings.TrimSpace(o.Value) == "" {
		return nil, errors.New("value is required")
	}
	if o.Address != nil && strings.TrimSpace(*o.Address) == "" {
		return nil, errors.New("address must be nonblank or null")
	}
	if o.RawJSON {
		if _, err := object([]byte(o.Value)); err != nil {
			return nil, errors.New("rawJson value must encode a JSON object")
		}
	}
	if len(o.AdditionalWrites) > 256 {
		return nil, errors.New("at most 256 additionalWrites are allowed")
	}
	for _, w := range o.AdditionalWrites {
		if _, err := object(w); err != nil {
			return nil, errors.New("each additionalWrites entry must be an object")
		}
	}
	if o.AdditionalWrites == nil {
		o.AdditionalWrites = []json.RawMessage{}
	}
	return json.Marshal(o)
}

func validatePreset(data []byte) ([]byte, error) {
	preset, err := object(data)
	if err != nil {
		return nil, err
	}
	metadata, err := object(preset["metadata"])
	if err != nil {
		return nil, errors.New("preset metadata must be an object")
	}
	for _, key := range []string{"id", "name"} {
		var value string
		if json.Unmarshal(metadata[key], &value) != nil || strings.TrimSpace(value) == "" || len(value) > 200 {
			return nil, fmt.Errorf("metadata.%s must contain 1–200 bytes", key)
		}
	}
	// The randomizer owns validation of its evolving settings and write syntax.
	return json.Marshal(json.RawMessage(data))
}
