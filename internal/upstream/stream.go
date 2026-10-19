package upstream

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"io"
	"sort"
)

var ErrInterrupted = errors.New("upstream_stream_interrupted")
var errSSEEOF = errors.New("sse_end_of_stream")

func (s *Stream) Events(yield func([]byte) error) error {
	if s.format == translator.FormatClaude {
		return readMessageEvents(s.Body, yield)
	}
	if s.format == translator.FormatGemini {
		return readGeminiEvents(s.Body, yield)
	}
	return readEvents(s.Body, yield)
}

func readEvents(reader io.Reader, yield func([]byte) error) error {
	output := make(map[int]json.RawMessage)
	outputBytes := 0
	err := readSSE(reader, func(raw []byte) (bool, error) {
		if bytes.Equal(raw, []byte("[DONE]")) {
			return false, ErrInterrupted
		}
		var event struct {
			Type        string          `json:"type"`
			OutputIndex *int            `json:"output_index"`
			Item        json.RawMessage `json:"item"`
			Response    json.RawMessage `json:"response"`
		}
		if json.Unmarshal(raw, &event) != nil || event.Type == "" {
			return false, ErrResponse
		}
		if event.Type == "error" || event.Type == "response.failed" {
			if event.Type == "response.failed" {
				if err := yield(raw); err != nil {
					return false, err
				}
			}
			return false, ErrInterrupted
		}
		if event.Type == "response.output_item.done" && len(event.Item) > 0 {
			index := len(output)
			if event.OutputIndex != nil {
				index = *event.OutputIndex
			}
			if index < 0 || index > 4095 {
				return false, ErrResponse
			}
			outputBytes += len(event.Item) - len(output[index])
			if outputBytes > MaxBody {
				return false, ErrResponse
			}
			output[index] = bytes.Clone(event.Item)
		}
		terminal := event.Type == "response.completed" || event.Type == "response.incomplete"
		if terminal && len(output) > 0 {
			var response map[string]json.RawMessage
			if json.Unmarshal(event.Response, &response) != nil || response == nil {
				return false, ErrResponse
			}
			var existing []json.RawMessage
			if len(response["output"]) > 0 && json.Unmarshal(response["output"], &existing) != nil {
				return false, ErrResponse
			}
			// Some Codex streams emit final items separately and leave response.output empty.
			if len(existing) == 0 {
				indexes := make([]int, 0, len(output))
				for index := range output {
					indexes = append(indexes, index)
				}
				sort.Ints(indexes)
				items := make([]json.RawMessage, 0, len(indexes))
				for _, index := range indexes {
					items = append(items, output[index])
				}
				response["output"], _ = json.Marshal(items)
				var updated map[string]json.RawMessage
				if json.Unmarshal(raw, &updated) != nil {
					return false, ErrResponse
				}
				updated["response"], _ = json.Marshal(response)
				raw, _ = json.Marshal(updated)
				if len(raw) > MaxBody {
					return false, ErrResponse
				}
			}
		}
		if terminal && len(event.Response) == 0 {
			return false, ErrResponse
		}
		if err := yield(raw); err != nil {
			return false, err
		}
		return terminal, nil
	})
	return requireTerminal(err)
}

func requireTerminal(err error) error {
	if errors.Is(err, errSSEEOF) {
		return ErrInterrupted
	}
	return err
}

// Each protocol owns its terminal/error semantics; framing and memory limits are shared.
func readSSE(reader io.Reader, consume func([]byte) (bool, error)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), MaxBody)
	var data bytes.Buffer
	emit := func() (bool, error) {
		if data.Len() == 0 {
			return false, nil
		}
		done, err := consume(bytes.TrimSpace(data.Bytes()))
		data.Reset()
		return done, err
	}
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			done, err := emit()
			if err != nil {
				return err
			}
			if done {
				return nil
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			value := line[5:]
			if len(value) > 0 && value[0] == ' ' {
				value = value[1:]
			}
			if data.Len()+len(value)+1 > MaxBody {
				return ErrResponse
			}
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.Write(value)
		}
	}
	if scanner.Err() != nil {
		var rejected *UpstreamError
		if errors.As(scanner.Err(), &rejected) {
			return rejected
		}
		return ErrResponse
	}
	if data.Len() > 0 {
		done, err := emit()
		if err != nil {
			return err
		}
		if done {
			return nil
		}
	}
	return errSSEEOF
}
