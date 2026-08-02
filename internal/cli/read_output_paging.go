package cli

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

func readOutputPageEnd(content string, offset, limit int) int {
	remaining := len(content) - offset
	end := offset + min(limit, remaining)
	for end > offset && end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	return end
}

func readOutputPageBoundaries(content string, offset, limit int) []int {
	end := readOutputPageEnd(content, offset, limit)
	boundaries := []int{offset}
	for cursor := offset; cursor < end; {
		_, size := utf8.DecodeRuneInString(content[cursor:])
		cursor += size
		boundaries = append(boundaries, cursor)
	}
	return boundaries
}

func readOutputPagePayload(ref string, originalBytes, offset, limit, end int, content string) readOutputPayload {
	hasMore := end < len(content)
	payload := readOutputPayload{
		Status:        "ok",
		Ref:           ref,
		Kind:          "output",
		Bytes:         originalBytes,
		Offset:        offset,
		Limit:         limit,
		ReturnedBytes: end - offset,
		HasMore:       hasMore,
		Truncated:     hasMore,
		ContentIsData: true,
		Note:          readOutputDataNote,
		Content:       content[offset:end],
	}
	if hasMore {
		next := end
		payload.NextOffset = &next
	}
	return payload
}

func marshalReadOutputPayload(payload readOutputPayload) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (t *readOutputTool) pageResponse(ref string, originalBytes int, offset, limit int, content string) (string, error) {
	boundaries := readOutputPageBoundaries(content, offset, limit)
	end := boundaries[len(boundaries)-1]
	if end == offset && offset < len(content) {
		return "", fmt.Errorf("read_output: limit cannot include the next character")
	}
	cap := t.resultLimit()
	empty, err := marshalReadOutputPayload(readOutputPagePayload(ref, originalBytes, offset, limit, offset, content))
	if err != nil {
		return "", err
	}
	if len(empty) > cap {
		return "", fmt.Errorf("read_output: result cap %d is too small for response framing", cap)
	}
	low, high := 0, len(boundaries)-1
	for low < high {
		mid := low + (high-low+1)/2
		candidate := boundaries[mid]
		encoded, err := marshalReadOutputPayload(readOutputPagePayload(ref, originalBytes, offset, limit, candidate, content))
		if err != nil {
			return "", err
		}
		if len(encoded) <= cap {
			low = mid
		} else {
			high = mid - 1
		}
	}
	end = boundaries[low]
	if end == offset && offset < len(content) {
		return "", fmt.Errorf("read_output: result cap %d cannot include the next character", cap)
	}
	return marshalReadOutputPayload(readOutputPagePayload(ref, originalBytes, offset, limit, end, content))
}
