package plexhooks

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrEmptyPayload signals that a webhook payload contained no data.
var ErrEmptyPayload = errors.New("plexhooks: empty webhook payload")

// Decoder abstracts how raw payload bytes are decoded into Go structs.
// Implementations must be safe for concurrent use.
type Decoder interface {
	Decode(data []byte, v interface{}) error
}

type decoderFunc func([]byte, interface{}) error

func (f decoderFunc) Decode(data []byte, v interface{}) error {
	return f(data, v)
}

// Parser converts the raw Plex webhook payloads into strongly-typed structs.
// A Parser is safe for concurrent use provided the configured Decoder is.
type Parser struct {
	decoder Decoder
}

// Option configures Parser construction.
type Option func(*Parser)

// WithDecoder overrides the default JSON decoder used by Parser.
func WithDecoder(dec Decoder) Option {
	return func(p *Parser) {
		if dec != nil {
			p.decoder = dec
		}
	}
}

// NewParser constructs a Parser that can convert webhook payloads. By default
// it uses encoding/json for decoding; callers may provide options to customise
// behaviour.
func NewParser(options ...Option) *Parser {
	parser := &Parser{
		decoder: decoderFunc(json.Unmarshal),
	}
	for _, opt := range options {
		opt(parser)
	}
	return parser
}

// ParseWebhook converts a raw Plex webhook payload into a Webhook struct.
// Prefer Parser.Parse when multiple payloads must be processed efficiently.
func ParseWebhook(payload []byte) (*Webhook, error) {
	return NewParser().Parse(payload)
}

// Parse converts a raw Plex webhook payload into a Webhook struct.
func (p *Parser) Parse(payload []byte) (*Webhook, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, ErrEmptyPayload
	}

	var hook Webhook
	if err := p.decoder.Decode(payload, &hook); err != nil {
		return nil, fmt.Errorf("plexhooks: decode webhook: %w", err)
	}

	return &hook, nil
}
