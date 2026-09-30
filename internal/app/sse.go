package app

import (
	"bytes"
	"encoding/json"
)

// Bounded framing preserves exact bytes; oversized events bypass observation.
type sseObserver struct {
	max      int
	buf      []byte
	tail     []byte
	oversize bool
	Skipped  bool
	frame    func([]byte) error
	raw      func([]byte) error
}

func newSSE(max int, frame, raw func([]byte) error) *sseObserver {
	return &sseObserver{max: max, frame: frame, raw: raw}
}
func (s *sseObserver) Feed(b []byte) error {
	for _, v := range b {
		s.buf = append(s.buf, v)
		s.tail = append(s.tail, v)
		if len(s.tail) > 4 {
			s.tail = s.tail[1:]
		}
		end := bytes.HasSuffix(s.tail, []byte("\n\n")) || bytes.HasSuffix(s.tail, []byte("\r\n\r\n")) || bytes.HasSuffix(s.tail, []byte("\r\r"))
		if len(s.buf) > s.max {
			s.oversize = true
			s.Skipped = true
			if err := s.raw(s.buf); err != nil {
				return err
			}
			s.buf = s.buf[:0]
		}
		if end {
			var err error
			if s.oversize {
				err = s.raw(s.buf)
			} else {
				err = s.frame(s.buf)
			}
			s.buf = s.buf[:0]
			s.tail = s.tail[:0]
			s.oversize = false
			if err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *sseObserver) End() error {
	if len(s.buf) > 0 {
		return s.raw(s.buf)
	}
	return nil
}
func sseData(b []byte) ([]byte, string) {
	b = bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))
	b = bytes.ReplaceAll(b, []byte("\r"), []byte("\n"))
	var data []byte
	event := ""
	for _, line := range bytes.Split(b, []byte("\n")) {
		key, value, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			continue
		}
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(key) {
		case "data":
			if data != nil {
				data = append(data, '\n')
			}
			data = append(data, value...)
		case "event":
			event = string(value)
		}
	}
	if event == "" {
		var v struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(data, &v)
		event = v.Type
	}
	return data, event
}
