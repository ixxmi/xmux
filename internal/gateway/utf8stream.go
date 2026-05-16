package gateway

import (
	"strings"
	"unicode/utf8"
)

type utf8StreamDecoder struct {
	pending []byte
}

type UTF8StreamDecoder = utf8StreamDecoder

func (d *utf8StreamDecoder) Push(chunk []byte) string {
	if len(chunk) == 0 {
		return ""
	}

	data := chunk
	if len(d.pending) > 0 {
		merged := make([]byte, 0, len(d.pending)+len(chunk))
		merged = append(merged, d.pending...)
		merged = append(merged, chunk...)
		data = merged
		d.pending = d.pending[:0]
	}

	complete, pending := splitCompleteUTF8(data)
	if len(pending) > 0 {
		d.pending = append(d.pending[:0], pending...)
	} else {
		d.pending = d.pending[:0]
	}
	return validUTF8String(complete)
}

func (d *utf8StreamDecoder) Flush() string {
	if len(d.pending) == 0 {
		return ""
	}
	out := validUTF8String(d.pending)
	d.pending = nil
	return out
}

func splitCompleteUTF8(data []byte) ([]byte, []byte) {
	if len(data) == 0 || utf8.Valid(data) {
		return data, nil
	}

	searchStart := len(data) - (utf8.UTFMax - 1)
	if searchStart < 0 {
		searchStart = 0
	}
	for i := len(data) - 1; i >= searchStart; i-- {
		if !utf8.RuneStart(data[i]) {
			continue
		}
		suffix := data[i:]
		if utf8.FullRune(suffix) {
			break
		}
		prefix := data[:i]
		if utf8.Valid(prefix) {
			return prefix, append([]byte(nil), suffix...)
		}
		return []byte(validUTF8String(prefix)), append([]byte(nil), suffix...)
	}

	return []byte(validUTF8String(data)), nil
}

func validUTF8String(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	if utf8.Valid(data) {
		return string(data)
	}
	return strings.ToValidUTF8(string(data), "\uFFFD")
}

func trimUTF8Replay(data []byte, max int) []byte {
	if max <= 0 || len(data) <= max {
		return data
	}
	start := len(data) - max
	for start < len(data) && !utf8.RuneStart(data[start]) {
		start++
	}
	if start >= len(data) {
		return nil
	}
	return data[start:]
}
