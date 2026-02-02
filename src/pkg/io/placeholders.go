package io

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ValueRetail/vrsky/pkg/component"
	"github.com/ValueRetail/vrsky/pkg/envelope"
)

// HTTPInput placeholder (for future implementation)
type HTTPInput struct{}

func NewHTTPInput(configJSON json.RawMessage) (*HTTPInput, error) {
	return nil, fmt.Errorf("HTTPInput not yet implemented")
}

func (h *HTTPInput) Start(ctx context.Context) error {
	return fmt.Errorf("HTTPInput not yet implemented")
}

func (h *HTTPInput) Read(ctx context.Context) (*envelope.Envelope, error) {
	return nil, fmt.Errorf("HTTPInput not yet implemented")
}

func (h *HTTPInput) Close() error {
	return nil
}

// FileInput placeholder (for future implementation)
type FileInput struct{}

func NewFileInput(configJSON json.RawMessage) (*FileInput, error) {
	return nil, fmt.Errorf("FileInput not yet implemented")
}

func (f *FileInput) Start(ctx context.Context) error {
	return fmt.Errorf("FileInput not yet implemented")
}

func (f *FileInput) Read(ctx context.Context) (*envelope.Envelope, error) {
	return nil, fmt.Errorf("FileInput not yet implemented")
}

func (f *FileInput) Close() error {
	return nil
}

// NATSOutput placeholder (for future implementation)
type NATSOutput struct{}

func NewNATSOutput(configJSON json.RawMessage) (*NATSOutput, error) {
	return nil, fmt.Errorf("NATSOutput not yet implemented")
}

func (n *NATSOutput) Start(ctx context.Context) error {
	return fmt.Errorf("NATSOutput not yet implemented")
}

func (n *NATSOutput) Write(ctx context.Context, env *envelope.Envelope) error {
	return fmt.Errorf("NATSOutput not yet implemented")
}

func (n *NATSOutput) Close() error {
	return nil
}

// FileOutput placeholder (for future implementation)
type FileOutput struct{}

func NewFileOutput(configJSON json.RawMessage) (*FileOutput, error) {
	return nil, fmt.Errorf("FileOutput not yet implemented")
}

func (f *FileOutput) Start(ctx context.Context) error {
	return fmt.Errorf("FileOutput not yet implemented")
}

func (f *FileOutput) Write(ctx context.Context, env *envelope.Envelope) error {
	return fmt.Errorf("FileOutput not yet implemented")
}

func (f *FileOutput) Close() error {
	return nil
}

// Verify interfaces are implemented
var (
	_ component.Input  = (*NATSInput)(nil)
	_ component.Input  = (*HTTPInput)(nil)
	_ component.Input  = (*FileInput)(nil)
	_ component.Output = (*HTTPOutput)(nil)
	_ component.Output = (*NATSOutput)(nil)
	_ component.Output = (*FileOutput)(nil)
)
