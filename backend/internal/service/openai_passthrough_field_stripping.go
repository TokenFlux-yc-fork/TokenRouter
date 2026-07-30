package service

import (
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"strings"

	infraerrors "github.com/TokenFlux/TokenRouter/internal/pkg/errors"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	openAIPassthroughStripFieldsExtraKey = "openai_passthrough_strip_fields"
	maxOpenAIPassthroughStripFields      = 32
	maxOpenAIPassthroughStripFieldLength = 160
)

var (
	defaultOpenAIPassthroughStripFields  = []string{"max_output_tokens"}
	openAIPassthroughStripPathPattern    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*(?:\[(?:\d+)?\])?(?:\.[A-Za-z_][A-Za-z0-9_-]*(?:\[(?:\d+)?\])?)*$`)
	openAIPassthroughStripSegmentPattern = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_-]*)(?:\[(\d*)\])?$`)
)

type openAIPassthroughStripPathSegment struct {
	name          string
	arraySelected bool
	wildcard      bool
	index         int
}

// normalizeOpenAIPassthroughStripFields validates the small JSON-path subset
// accepted by the passthrough field policy. Empty brackets select every item.
func normalizeOpenAIPassthroughStripFields(fields []string) ([]string, error) {
	if len(fields) > maxOpenAIPassthroughStripFields {
		return nil, fmt.Errorf("at most %d passthrough strip fields are allowed", maxOpenAIPassthroughStripFields)
	}

	normalized := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, raw := range fields {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		if len(path) > maxOpenAIPassthroughStripFieldLength || !openAIPassthroughStripPathPattern.MatchString(path) {
			return nil, fmt.Errorf("invalid passthrough strip field path %q", path)
		}
		segments, err := parseOpenAIPassthroughStripPath(path)
		if err != nil {
			return nil, err
		}
		if len(segments) == 1 {
			switch segments[0].name {
			case "model", "input", "stream":
				return nil, fmt.Errorf("passthrough strip field %q is required for request routing or response handling", path)
			}
		}
		if segments[len(segments)-1].arraySelected {
			return nil, fmt.Errorf("passthrough strip field %q must end at an object field", path)
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		normalized = append(normalized, path)
	}
	return normalized, nil
}

func parseOpenAIPassthroughStripPath(path string) ([]openAIPassthroughStripPathSegment, error) {
	parts := strings.Split(path, ".")
	segments := make([]openAIPassthroughStripPathSegment, 0, len(parts))
	for _, part := range parts {
		match := openAIPassthroughStripSegmentPattern.FindStringSubmatch(part)
		if len(match) != 3 {
			return nil, fmt.Errorf("invalid passthrough strip field path %q", path)
		}
		segment := openAIPassthroughStripPathSegment{name: match[1]}
		if strings.Contains(part, "[") {
			segment.arraySelected = true
			if match[2] == "" {
				segment.wildcard = true
			} else {
				index, err := strconv.Atoi(match[2])
				if err != nil || index < 0 {
					return nil, fmt.Errorf("invalid passthrough strip field path %q", path)
				}
				segment.index = index
			}
		}
		segments = append(segments, segment)
	}
	return segments, nil
}

func openAIPassthroughStripFieldsFromAny(raw any) ([]string, bool) {
	switch values := raw.(type) {
	case []string:
		return append([]string(nil), values...), true
	case []any:
		fields := make([]string, 0, len(values))
		for _, value := range values {
			field, ok := value.(string)
			if !ok {
				return nil, false
			}
			fields = append(fields, field)
		}
		return fields, true
	default:
		return nil, false
	}
}

func normalizeOpenAIPassthroughStripFieldsExtra(platform string, extra map[string]any) (map[string]any, error) {
	if platform != PlatformOpenAI || extra == nil {
		return extra, nil
	}
	raw, provided := extra[openAIPassthroughStripFieldsExtraKey]
	if !provided {
		return extra, nil
	}
	fields, ok := openAIPassthroughStripFieldsFromAny(raw)
	if !ok {
		return nil, infraerrors.BadRequest(
			"OPENAI_PASSTHROUGH_STRIP_FIELDS_INVALID",
			"openai_passthrough_strip_fields must be an array of JSON field paths",
		)
	}
	normalizedFields, err := normalizeOpenAIPassthroughStripFields(fields)
	if err != nil {
		return nil, infraerrors.BadRequest("OPENAI_PASSTHROUGH_STRIP_FIELDS_INVALID", err.Error())
	}
	normalized := maps.Clone(extra)
	normalized[openAIPassthroughStripFieldsExtraKey] = normalizedFields
	return normalized, nil
}

func normalizeGroupOpenAIPassthroughStripFields(platform string, fields *[]string) ([]string, error) {
	if platform != PlatformOpenAI {
		return []string{}, nil
	}
	if fields == nil {
		return append([]string(nil), defaultOpenAIPassthroughStripFields...), nil
	}
	return normalizeOpenAIPassthroughStripFields(*fields)
}

// resolveOpenAIPassthroughStripFields applies account-replaces-group semantics.
// A present empty account array intentionally disables proactive stripping.
func resolveOpenAIPassthroughStripFields(account *Account, group *Group) []string {
	if account != nil && account.Extra != nil {
		if raw, provided := account.Extra[openAIPassthroughStripFieldsExtraKey]; provided {
			if fields, ok := openAIPassthroughStripFieldsFromAny(raw); ok {
				if normalized, err := normalizeOpenAIPassthroughStripFields(fields); err == nil {
					return normalized
				}
			}
		}
	}
	if group != nil {
		if group.OpenAIPassthroughStripFields != nil {
			if normalized, err := normalizeOpenAIPassthroughStripFields(group.OpenAIPassthroughStripFields); err == nil {
				return normalized
			}
		}
		return append([]string(nil), defaultOpenAIPassthroughStripFields...)
	}
	return nil
}

func stripOpenAIPassthroughRequestFields(body []byte, fields []string) ([]byte, bool, error) {
	if len(body) == 0 || len(fields) == 0 {
		return body, false, nil
	}
	if !gjson.ValidBytes(body) {
		return nil, false, fmt.Errorf("invalid OpenAI passthrough request JSON")
	}
	normalized, err := normalizeOpenAIPassthroughStripFields(fields)
	if err != nil {
		return nil, false, err
	}

	result := append([]byte(nil), body...)
	changed := false
	for _, path := range normalized {
		segments, parseErr := parseOpenAIPassthroughStripPath(path)
		if parseErr != nil {
			return nil, false, parseErr
		}
		concretePaths := expandOpenAIPassthroughStripPaths(gjson.ParseBytes(result), segments, "")
		for _, concretePath := range concretePaths {
			next, deleteErr := sjson.DeleteBytes(result, concretePath)
			if deleteErr != nil {
				return nil, false, fmt.Errorf("delete passthrough field %s: %w", path, deleteErr)
			}
			result = next
			changed = true
		}
	}
	return result, changed, nil
}

func expandOpenAIPassthroughStripPaths(current gjson.Result, segments []openAIPassthroughStripPathSegment, prefix string) []string {
	if len(segments) == 0 {
		if current.Exists() {
			return []string{prefix}
		}
		return nil
	}

	segment := segments[0]
	child := current.Get(segment.name)
	childPath := segment.name
	if prefix != "" {
		childPath = prefix + "." + segment.name
	}
	if !segment.arraySelected {
		return expandOpenAIPassthroughStripPaths(child, segments[1:], childPath)
	}
	if !child.IsArray() {
		return nil
	}
	items := child.Array()
	if !segment.wildcard {
		if segment.index >= len(items) {
			return nil
		}
		indexedPath := fmt.Sprintf("%s.%d", childPath, segment.index)
		return expandOpenAIPassthroughStripPaths(items[segment.index], segments[1:], indexedPath)
	}

	paths := make([]string, 0)
	for index, item := range items {
		indexedPath := fmt.Sprintf("%s.%d", childPath, index)
		paths = append(paths, expandOpenAIPassthroughStripPaths(item, segments[1:], indexedPath)...)
	}
	return paths
}
