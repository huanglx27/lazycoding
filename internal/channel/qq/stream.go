package qq

import (
	"strings"
	"time"
	"unicode/utf8"
)

const (
	flushInterval = 1500 * time.Millisecond
	flushChars    = 400
	maxSegments   = 4 // reserve 1 segment for the final marker
)

// streamSender buffers streaming text chunks and sends them to QQ in segments.
type streamSender struct {
	client  *apiClient
	openid  string
	msgID   string
	buf     strings.Builder
	segs    int
	done    chan struct{}
	chunks  chan string
}

func newStreamSender(client *apiClient, openid, msgID string) *streamSender {
	s := &streamSender{
		client: client,
		openid: openid,
		msgID:  msgID,
		done:   make(chan struct{}),
		chunks: make(chan string, 64),
	}
	go s.loop()
	return s
}

// write enqueues a text chunk from the Claude stream.
func (s *streamSender) write(chunk string) {
	s.chunks <- chunk
}

// close signals end-of-stream and waits for the final flush to complete.
func (s *streamSender) close() {
	close(s.chunks)
	<-s.done
}

func (s *streamSender) loop() {
	defer close(s.done)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()

	for {
		select {
		case chunk, ok := <-s.chunks:
			if !ok {
				s.flush(true)
				return
			}
			s.buf.WriteString(chunk)
			if utf8.RuneCountInString(s.buf.String()) >= flushChars {
				s.flush(false)
			}
		case <-ticker.C:
			s.flush(false)
		}
	}
}

func (s *streamSender) flush(final bool) {
	text := s.buf.String()
	if text == "" && !final {
		return
	}
	s.buf.Reset()

	if final {
		text += "\n───"
	}

	s.client.sendC2CMessage(s.openid, text, s.msgID) //nolint:errcheck
	s.segs++
}
