package filter

import (
	"encoding/json"
	"encoding/xml"
	"fmt"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// MessageParser handles parsing of different message formats
type MessageParser struct{}

// NewMessageParser creates a new message parser
func NewMessageParser() *MessageParser {
	return &MessageParser{}
}

// ParseMessage parses a message from an envelope
// Supports JSON and XML content types
func (mp *MessageParser) ParseMessage(env *envelope.Envelope) (interface{}, error) {
	if env == nil {
		return nil, fmt.Errorf("envelope cannot be nil")
	}

	if len(env.Payload) == 0 {
		return nil, fmt.Errorf("payload is empty")
	}

	// Route based on content type
	switch env.ContentType {
	case "application/json", "":
		// Default to JSON
		return mp.parseJSON(env.Payload)
	case "application/xml", "text/xml":
		return mp.parseXML(env.Payload)
	case "text/plain":
		return string(env.Payload), nil
	default:
		// Try JSON first, fall back to string
		if payload, err := mp.parseJSON(env.Payload); err == nil {
			return payload, nil
		}
		return string(env.Payload), nil
	}
}

// parseJSON parses JSON payload
func (mp *MessageParser) parseJSON(payload []byte) (interface{}, error) {
	var result interface{}
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("parse json: %w", err)
	}
	return result, nil
}

// parseXML parses XML payload and converts to a map
func (mp *MessageParser) parseXML(payload []byte) (interface{}, error) {
	// Parse XML to map structure
	// Note: xml.Unmarshal converts XML to map[string]interface{}
	var result interface{}
	if err := xml.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("parse xml: %w", err)
	}

	return result, nil
}

// MessageExtractor extracts specific fields from parsed messages
type MessageExtractor struct{}

// NewMessageExtractor creates a new message extractor
func NewMessageExtractor() *MessageExtractor {
	return &MessageExtractor{}
}

// Extract extracts a value from a message using a path
// Supports dot notation (e.g., "user.name")
func (me *MessageExtractor) Extract(message interface{}, path string) (interface{}, error) {
	if path == "" {
		return message, nil
	}

	ce := NewConditionEngine()
	return ce.GetFieldValue(message, path)
}

// ExtractMultiple extracts multiple values from a message
func (me *MessageExtractor) ExtractMultiple(message interface{}, paths ...string) (map[string]interface{}, error) {
	result := make(map[string]interface{})
	ce := NewConditionEngine()

	for _, path := range paths {
		value, err := ce.GetFieldValue(message, path)
		if err != nil {
			return nil, fmt.Errorf("extract %s: %w", path, err)
		}
		result[path] = value
	}

	return result, nil
}

// PayloadValidator validates message payloads
type PayloadValidator struct{}

// NewPayloadValidator creates a new payload validator
func NewPayloadValidator() *PayloadValidator {
	return &PayloadValidator{}
}

// ValidateNotEmpty checks if payload is not empty
func (pv *PayloadValidator) ValidateNotEmpty(env *envelope.Envelope) error {
	if env == nil {
		return fmt.Errorf("envelope cannot be nil")
	}
	if len(env.Payload) == 0 {
		return fmt.Errorf("payload is empty")
	}
	return nil
}

// ValidateContentType checks if the content type is valid
func (pv *PayloadValidator) ValidateContentType(env *envelope.Envelope, validTypes []string) error {
	if env == nil {
		return fmt.Errorf("envelope cannot be nil")
	}

	// Allow empty content type (default to JSON)
	if env.ContentType == "" {
		return nil
	}

	for _, validType := range validTypes {
		if env.ContentType == validType {
			return nil
		}
	}

	return fmt.Errorf("invalid content type: %s", env.ContentType)
}

// ValidatePayloadSize checks if payload size is within limits
func (pv *PayloadValidator) ValidatePayloadSize(env *envelope.Envelope, maxSize int64) error {
	if env == nil {
		return fmt.Errorf("envelope cannot be nil")
	}

	if int64(len(env.Payload)) > maxSize {
		return fmt.Errorf("payload size %d exceeds maximum %d", len(env.Payload), maxSize)
	}

	return nil
}

// ValidateParseable checks if the payload can be parsed as JSON
func (pv *PayloadValidator) ValidateParseable(env *envelope.Envelope) error {
	parser := NewMessageParser()
	_, err := parser.ParseMessage(env)
	if err != nil {
		return fmt.Errorf("payload is not parseable: %w", err)
	}
	return nil
}
