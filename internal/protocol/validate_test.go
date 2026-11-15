package protocol

import "testing"

func TestValidateRequestParameters(t *testing.T) {
	cases := []struct {
		name     string
		protocol Protocol
		body     map[string]any
		wantErr  bool
	}{
		{name: "chat temperature in range", protocol: Chat, body: map[string]any{"temperature": 1.5}},
		{name: "chat temperature at max", protocol: Chat, body: map[string]any{"temperature": float64(2)}},
		{name: "chat temperature above max", protocol: Chat, body: map[string]any{"temperature": 9.0}, wantErr: true},
		{name: "chat temperature below min", protocol: Chat, body: map[string]any{"temperature": -0.1}, wantErr: true},
		{name: "chat temperature as string", protocol: Chat, body: map[string]any{"temperature": "1"}, wantErr: true},
		{name: "null temperature means default", protocol: Chat, body: map[string]any{"temperature": nil}},
		{name: "anthropic temperature above max", protocol: Anthropic, body: map[string]any{"temperature": 1.5}, wantErr: true},
		{name: "anthropic temperature in range", protocol: Anthropic, body: map[string]any{"temperature": 1.0}},
		{name: "top_p above max", protocol: Chat, body: map[string]any{"top_p": 1.5}, wantErr: true},
		{name: "top_p at bounds", protocol: Responses, body: map[string]any{"top_p": 1.0}},
		{name: "chat max_tokens negative", protocol: Chat, body: map[string]any{"max_tokens": -1}, wantErr: true},
		{name: "chat max_tokens zero", protocol: Chat, body: map[string]any{"max_tokens": 0}, wantErr: true},
		{name: "chat max_tokens fraction", protocol: Chat, body: map[string]any{"max_tokens": 1.5}, wantErr: true},
		{name: "chat max_completion_tokens negative", protocol: Chat, body: map[string]any{"max_completion_tokens": -1}, wantErr: true},
		{name: "chat max_tokens ok", protocol: Chat, body: map[string]any{"max_tokens": 16}},
		{name: "responses max_output_tokens zero", protocol: Responses, body: map[string]any{"max_output_tokens": 0}, wantErr: true},
		{name: "responses max_output_tokens ok", protocol: Responses, body: map[string]any{"max_output_tokens": 15}},
		{name: "anthropic max_tokens zero", protocol: Anthropic, body: map[string]any{"max_tokens": 0}, wantErr: true},
		{name: "int form accepted", protocol: Chat, body: map[string]any{"temperature": 1, "max_tokens": 32}},
		{name: "empty body", protocol: Chat, body: map[string]any{}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateRequestParameters(test.protocol, test.body)
			if test.wantErr && err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
