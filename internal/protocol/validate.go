package protocol

import (
	"encoding/json"
	"fmt"
	"math"
)

// ValidateRequestParameters rejects sampling values the client's own protocol
// defines out of range. The gateway used to forward them unchanged: the free
// tier ignores temperature and top_p instead of failing, so temperature=9.0
// still answered 200 and looked like a deterministic replay, and max_tokens=-1
// travelled all the way to the upstream's deserializer. A native endpoint
// answers 400 invalid_request_error for both, which is what a client expects
// and what keeps a bad probe from being read as model behaviour.
func ValidateRequestParameters(protocol Protocol, input map[string]any) error {
	if err := validateRange(input, "temperature", temperatureRange(protocol)); err != nil {
		return err
	}
	if err := validateRange(input, "top_p", [2]float64{0, 1}); err != nil {
		return err
	}
	for _, name := range tokenLimitFields(protocol) {
		if err := validateTokenLimit(input, name); err != nil {
			return err
		}
	}
	return nil
}

// temperatureRange is the range each protocol documents. Anthropic caps
// temperature at 1; the OpenAI protocols allow up to 2.
func temperatureRange(protocol Protocol) [2]float64 {
	if protocol == Anthropic {
		return [2]float64{0, 1}
	}
	return [2]float64{0, 2}
}

// tokenLimitFields names the output-budget fields of each protocol. A body may
// carry only one of the Chat spellings; both are checked when both are sent.
func tokenLimitFields(protocol Protocol) []string {
	switch protocol {
	case Chat:
		return []string{"max_tokens", "max_completion_tokens"}
	case Responses:
		return []string{"max_output_tokens"}
	case Anthropic:
		return []string{"max_tokens"}
	default:
		return nil
	}
}

// validateRange accepts an absent or null value (the client means "default")
// and any JSON number, including the int form the bridge builds internally.
func validateRange(input map[string]any, name string, bounds [2]float64) error {
	raw, ok := input[name]
	if !ok || raw == nil {
		return nil
	}
	value, ok := numberValue(raw)
	if !ok {
		return fmt.Errorf("invalid %s: expected a number", name)
	}
	if value < bounds[0] || value > bounds[1] {
		return fmt.Errorf("invalid %s: expected a number between %g and %g, got %g", name, bounds[0], bounds[1], value)
	}
	return nil
}

func validateTokenLimit(input map[string]any, name string) error {
	raw, ok := input[name]
	if !ok || raw == nil {
		return nil
	}
	value, ok := numberValue(raw)
	if !ok || value != math.Trunc(value) {
		return fmt.Errorf("invalid %s: expected an integer", name)
	}
	if value < 1 {
		return fmt.Errorf("invalid %s: expected a positive integer, got %g", name, value)
	}
	return nil
}

func numberValue(raw any) (float64, bool) {
	switch value := raw.(type) {
	case float64:
		return value, true
	case int:
		return float64(value), true
	case json.Number:
		number, err := value.Float64()
		if err != nil {
			return 0, false
		}
		return number, true
	default:
		return 0, false
	}
}
