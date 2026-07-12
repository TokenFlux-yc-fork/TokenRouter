package service

import "net/http"

type openAIResponseWriterUnwrapper interface {
	Unwrap() http.ResponseWriter
}

// flushOpenAIResponseWriter unwraps response-writer adapters that only expose
// the legacy error-less Flush method. This lets the standard library reach a
// lower-level FlushError implementation and report a disconnected client.
func flushOpenAIResponseWriter(writer http.ResponseWriter) error {
	if writer == nil {
		return http.ErrNotSupported
	}
	current := writer
	for {
		if _, ok := current.(interface{ FlushError() error }); ok {
			break
		}
		unwrapper, ok := current.(openAIResponseWriterUnwrapper)
		if !ok {
			break
		}
		next := unwrapper.Unwrap()
		if next == nil || next == current {
			break
		}
		current = next
	}
	return http.NewResponseController(current).Flush()
}
