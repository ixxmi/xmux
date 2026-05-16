package gateway

import "testing"

func TestUTF8StreamDecoderKeepsSplitRunes(t *testing.T) {
	var decoder utf8StreamDecoder
	value := "───中文────"
	raw := []byte(value)

	var got string
	for _, chunk := range [][]byte{
		raw[:1],
		raw[1:5],
		raw[5:8],
		raw[8:12],
		raw[12:],
	} {
		got += decoder.Push(chunk)
	}
	got += decoder.Flush()

	if got != value {
		t.Fatalf("decoded output = %q, want %q", got, value)
	}
}

func TestUTF8StreamDecoderReplacesInvalidBytes(t *testing.T) {
	var decoder utf8StreamDecoder
	got := decoder.Push([]byte{'o', 'k', 0xff, '\n'})
	got += decoder.Flush()

	if got != "ok\uFFFD\n" {
		t.Fatalf("decoded output = %q", got)
	}
}

func TestTrimUTF8ReplayDoesNotStartInsideRune(t *testing.T) {
	value := []byte("ab──")
	got := string(trimUTF8Replay(value, 4))
	if got != "─" {
		t.Fatalf("trimmed replay = %q, want %q", got, "─")
	}
}
