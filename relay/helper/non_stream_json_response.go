package helper

import (
	"bufio"
	"io"
	"mime"
	"net/http"
)

// NormalizeNonStreamJSONResponse corrects an SSE content type when the body
// starts with a JSON object. Call it only for requests that did not ask to stream.
// Probing is bounded by the reader's capacity; all bytes and Close are preserved.
func NormalizeNonStreamJSONResponse(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "text/event-stream" {
		return
	}

	body := resp.Body
	reader := bufio.NewReader(body)
	resp.Body = struct {
		io.Reader
		io.Closer
	}{Reader: reader, Closer: body}

	for n := 1; n <= reader.Size(); n++ {
		prefix, err := reader.Peek(n)
		if err != nil {
			return
		}
		switch prefix[n-1] {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			resp.Header.Set("Content-Type", "application/json")
		}
		return
	}
}
