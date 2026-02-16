package filter

import (
	"encoding/json"
	"testing"

	"github.com/ValueRetail/vrsky/pkg/envelope"
)

func TestMessageParser_ParseJSON(t *testing.T) {
	parser := NewMessageParser()

	tests := []struct {
		name    string
		payload []byte
		want    interface{}
		wantErr bool
	}{
		{
			"valid json object",
			[]byte(`{"name":"John","age":30}`),
			map[string]interface{}{"name": "John", "age": float64(30)},
			false,
		},
		{
			"valid json array",
			[]byte(`[1,2,3]`),
			[]interface{}{float64(1), float64(2), float64(3)},
			false,
		},
		{
			"invalid json",
			[]byte(`{invalid}`),
			nil,
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parser.parseJSON(tt.payload)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseJSON error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil {
				// Compare as JSON strings for consistency
				gotJSON, _ := json.Marshal(got)
				wantJSON, _ := json.Marshal(tt.want)
				if string(gotJSON) != string(wantJSON) {
					t.Errorf("parseJSON got = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestMessageParser_ParseMessage(t *testing.T) {
	parser := NewMessageParser()

	tests := []struct {
		name        string
		envelope    *envelope.Envelope
		wantErr     bool
		wantPayload bool
	}{
		{
			"valid json envelope",
			&envelope.Envelope{
				ContentType: "application/json",
				Payload:     []byte(`{"status":"ok"}`),
			},
			false,
			true,
		},
		{
			"empty content type defaults to json",
			&envelope.Envelope{
				ContentType: "",
				Payload:     []byte(`{"status":"ok"}`),
			},
			false,
			true,
		},
		{
			"plain text",
			&envelope.Envelope{
				ContentType: "text/plain",
				Payload:     []byte("Hello World"),
			},
			false,
			true,
		},
		{
			"empty payload",
			&envelope.Envelope{
				Payload: []byte{},
			},
			true,
			false,
		},
		{
			"nil envelope",
			nil,
			true,
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parser.ParseMessage(tt.envelope)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseMessage error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPayloadValidator_ValidateNotEmpty(t *testing.T) {
	validator := NewPayloadValidator()

	tests := []struct {
		name     string
		envelope *envelope.Envelope
		wantErr  bool
	}{
		{
			"valid payload",
			&envelope.Envelope{
				Payload: []byte("data"),
			},
			false,
		},
		{
			"empty payload",
			&envelope.Envelope{
				Payload: []byte{},
			},
			true,
		},
		{
			"nil envelope",
			nil,
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.ValidateNotEmpty(tt.envelope)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateNotEmpty error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPayloadValidator_ValidateContentType(t *testing.T) {
	validator := NewPayloadValidator()
	validTypes := []string{"application/json", "application/xml"}

	tests := []struct {
		name       string
		envelope   *envelope.Envelope
		validTypes []string
		wantErr    bool
	}{
		{
			"valid content type",
			&envelope.Envelope{
				ContentType: "application/json",
			},
			validTypes,
			false,
		},
		{
			"invalid content type",
			&envelope.Envelope{
				ContentType: "text/plain",
			},
			validTypes,
			true,
		},
		{
			"empty content type allowed",
			&envelope.Envelope{
				ContentType: "",
			},
			validTypes,
			false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.ValidateContentType(tt.envelope, tt.validTypes)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateContentType error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPayloadValidator_ValidatePayloadSize(t *testing.T) {
	validator := NewPayloadValidator()

	tests := []struct {
		name     string
		envelope *envelope.Envelope
		maxSize  int64
		wantErr  bool
	}{
		{
			"valid size",
			&envelope.Envelope{
				Payload: []byte("hello"),
			},
			100,
			false,
		},
		{
			"exceeds max size",
			&envelope.Envelope{
				Payload: []byte("hello world"),
			},
			5,
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.ValidatePayloadSize(tt.envelope, tt.maxSize)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePayloadSize error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
