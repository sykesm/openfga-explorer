package trace

import (
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/protobuf/types/known/structpb"
)

// ParseContext parses a check's request context (the parameters conditions
// read), given as a JSON object. Empty or blank text is no context (nil).
func ParseContext(text string) (*structpb.Struct, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		return nil, fmt.Errorf("the request context must be a JSON object: %w", err)
	}
	return structpb.NewStruct(m)
}
