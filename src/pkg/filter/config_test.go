package filter

import (
	"testing"
)

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			"valid config",
			&Config{
				FilterID:       "test",
				InputTopic:     "input",
				OutputTopic:    "output",
				RejectionTopic: "rejection",
				Rules:          []interface{}{map[string]interface{}{"id": "rule1"}},
			},
			false,
		},
		{
			"missing filter_id",
			&Config{
				InputTopic:     "input",
				OutputTopic:    "output",
				RejectionTopic: "rejection",
			},
			true,
		},
		{
			"missing input_topic",
			&Config{
				FilterID:       "test",
				OutputTopic:    "output",
				RejectionTopic: "rejection",
			},
			true,
		},
		{
			"missing output_topic",
			&Config{
				FilterID:       "test",
				InputTopic:     "input",
				RejectionTopic: "rejection",
			},
			true,
		},
		{
			"missing rejection_topic",
			&Config{
				FilterID:    "test",
				InputTopic:  "input",
				OutputTopic: "output",
			},
			true,
		},
		{
			"missing rules",
			&Config{
				FilterID:       "test",
				InputTopic:     "input",
				OutputTopic:    "output",
				RejectionTopic: "rejection",
				Rules:          []interface{}{},
			},
			true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
