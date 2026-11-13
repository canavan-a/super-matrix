package schema

import (
	"encoding/json"
	"fmt"
)

// converters[v] rewrites a version v document (decoded JSON) into version
// v+1. Bump CurrentVersion and add one here when a field is renamed,
// removed or changes meaning; a new optional field or collection whose
// absence keeps today's behaviour needs no new version (Since and a
// left-out collection being left alone cover it).
//
// Converting goes through map[string]any, which loses the order of keys,
// so a converted document plans its items alphabetically.
var converters = map[int]func(doc map[string]any) error{}

// upgrade converts a version-from document to CurrentVersion.
func upgrade(js []byte, from int) ([]byte, error) {
	if from == CurrentVersion {
		return js, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(js, &doc); err != nil {
		return nil, errAt("", "%v", err)
	}
	for v := from; v < CurrentVersion; v++ {
		conv, ok := converters[v]
		if !ok {
			return nil, errAt("version", "no way to convert version %d documents", v)
		}
		if err := conv(doc); err != nil {
			return nil, errAt("", "converting from version %d: %v", v, err)
		}
		doc["version"] = v + 1
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("converting: %w", err)
	}
	return out, nil
}
