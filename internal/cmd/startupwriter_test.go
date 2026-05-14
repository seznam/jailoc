package cmd

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartupWriter_PassthroughWhenNilStatus(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sw := newStartupWriter(&buf, nil, time.Second, nil, nil)

	n, err := sw.Write([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "hello", buf.String())
}

func TestStartupWriter_BuffersUntilAltScreen(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	sw := newStartupWriter(&buf, &status, 5*time.Second, nil, nil)

	// Write data without alt-screen escape — should be buffered.
	_, err := sw.Write([]byte("buffered data"))
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "data should be buffered, not written to underlying writer")

	// Alt-screen alone should not flush — need visible content.
	_, err = sw.Write([]byte("\x1b[?1049h"))
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "alt-screen alone should not flush")

	// Visible content triggers activation.
	content := strings.Repeat("x", activateMinVisible)
	_, err = sw.Write([]byte(content))
	require.NoError(t, err)
	assert.Equal(t, "buffered data\x1b[?1049h"+content, buf.String())
}

func TestStartupWriter_CrossBoundaryDetection(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	sw := newStartupWriter(&buf, &status, 5*time.Second, nil, nil)

	// First write ends mid-sequence: \x1b[?10 (5 of 8 bytes).
	_, err := sw.Write([]byte("somedata\x1b[?10"))
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "should still be buffering")

	// Second write completes the sequence but doesn't activate yet.
	_, err = sw.Write([]byte("49h\x1b[H\x1b[2J"))
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "should still buffer until enough post-alt content")

	// Visible content triggers activation.
	content := strings.Repeat("x", activateMinVisible)
	_, err = sw.Write([]byte(content))
	require.NoError(t, err)
	assert.Equal(t, "somedata\x1b[?1049h\x1b[H\x1b[2J"+content, buf.String())
}

func TestStartupWriter_EscapeOnlyDoesNotActivate(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	sw := newStartupWriter(&buf, &status, 5*time.Second, nil, nil)

	_, err := sw.Write([]byte("\x1b[?1049h"))
	require.NoError(t, err)

	_, err = sw.Write([]byte("\x1b[H\x1b[2J\x1b[?25l\x1b[1;1H\x1b[2K\x1b[2;1H\x1b[2K"))
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "escape-only content should not activate")

	_, err = sw.Write([]byte("A"))
	require.NoError(t, err)
	assert.NotEmpty(t, buf.String(), "first visible character should activate")
}

func TestStartupWriter_SlowStartWarning(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	sw := newStartupWriter(&buf, &status, 50*time.Millisecond, nil, nil)

	_, err := sw.Write([]byte("waiting for timeout"))
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "should be buffered before timeout")

	time.Sleep(150 * time.Millisecond)

	// Buffer should NOT be flushed — still buffering.
	assert.Empty(t, buf.String(), "timeout should not flush the buffer")

	// Status should contain the slow-start warning.
	assert.Contains(t, status.String(), "unusually long")

	// Close flushes normally.
	sw.Close()
	assert.Equal(t, "waiting for timeout", buf.String())
}

func TestStartupWriter_CloseFlushesBuffer(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	sw := newStartupWriter(&buf, &status, 5*time.Second, nil, nil)

	_, err := sw.Write([]byte("pending data"))
	require.NoError(t, err)
	assert.Empty(t, buf.String())

	sw.Close()
	assert.Equal(t, "pending data", buf.String())
}

func TestStartupWriter_ReadyPassthrough(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	sw := newStartupWriter(&buf, &status, 5*time.Second, nil, nil)

	// Trigger activation with alt-screen + substantial content.
	content := "\x1b[?1049h" + strings.Repeat("x", activateMinVisible)
	_, err := sw.Write([]byte(content))
	require.NoError(t, err)

	// Subsequent writes should pass through directly.
	buf.Reset()
	_, err = sw.Write([]byte("direct output"))
	require.NoError(t, err)
	assert.Equal(t, "direct output", buf.String())
}

func TestStartupWriter_LoadingMessageErased(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	sw := newStartupWriter(&buf, &status, 5*time.Second, nil, nil)

	// Status should have the loading message from constructor.
	assert.Equal(t, "Starting OpenCode...", status.String())

	// Trigger activate via Close.
	sw.Close()

	// Status should now also have the erase sequence.
	assert.Equal(t, "Starting OpenCode...\x1b[2K\r", status.String())
}

func TestStartupWriter_LogMessageAppearsInStatus(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	sw := newStartupWriter(&buf, &status, 5*time.Second, pr, func() {})

	go func() {
		_, _ = pw.Write([]byte("time=2024-01-01T00:00:00Z level=INFO msg=\"Loading config\"\n"))
		_ = pw.Close()
	}()

	select {
	case <-sw.logDone:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not exit")
	}

	assert.Contains(t, status.String(), "Loading config")
	assert.NotContains(t, status.String(), "time=")
}

func TestStartupWriter_QuotedMsgWithSpaces(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	sw := newStartupWriter(&buf, &status, 5*time.Second, pr, func() {})

	go func() {
		_, _ = pw.Write([]byte("level=DEBUG msg=\"LSP server is ready\" component=lsp\n"))
		_ = pw.Close()
	}()

	select {
	case <-sw.logDone:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not exit")
	}

	assert.Contains(t, status.String(), "LSP server is ready")
}

func TestStartupWriter_UnquotedMsgExtracted(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	sw := newStartupWriter(&buf, &status, 5*time.Second, pr, func() {})

	go func() {
		_, _ = pw.Write([]byte("time=2024 level=INFO msg=connecting\n"))
		_ = pw.Close()
	}()

	select {
	case <-sw.logDone:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not exit")
	}

	assert.Contains(t, status.String(), "connecting")
}

func TestStartupWriter_NonLogfmtLineSkipped(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	sw := newStartupWriter(&buf, &status, 5*time.Second, pr, func() {})

	go func() {
		_, _ = pw.Write([]byte("Setting up iptables rules...\n"))
		_ = pw.Close()
	}()

	select {
	case <-sw.logDone:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not exit")
	}

	assert.Equal(t, "Starting OpenCode...", status.String())
}

func TestStartupWriter_EmptyMsgSkipped(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	sw := newStartupWriter(&buf, &status, 5*time.Second, pr, func() {})

	go func() {
		_, _ = pw.Write([]byte("level=INFO msg=\"\"\n"))
		_ = pw.Close()
	}()

	select {
	case <-sw.logDone:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not exit")
	}

	assert.Equal(t, "Starting OpenCode...", status.String())
}

func TestStartupWriter_LongMsgTruncated(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	sw := newStartupWriter(&buf, &status, 5*time.Second, pr, func() {})

	longMsg := strings.Repeat("x", 200)
	go func() {
		_, _ = pw.Write([]byte("level=INFO msg=\"" + longMsg + "\"\n"))
		_ = pw.Close()
	}()

	select {
	case <-sw.logDone:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not exit")
	}

	statusStr := status.String()
	afterInitial := strings.TrimPrefix(statusStr, "Starting OpenCode...")
	afterErase := strings.TrimPrefix(afterInitial, "\x1b[2K\r")
	assert.LessOrEqual(t, len([]rune(afterErase)), 80)
	assert.Equal(t, strings.Repeat("x", 79)+"…", afterErase)
}

func TestStartupWriter_NilLogReaderNoPanic(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	sw := newStartupWriter(&buf, &status, 5*time.Second, nil, nil)

	assert.Equal(t, "Starting OpenCode...", status.String())

	sw.Close()

	assert.Contains(t, status.String(), "\x1b[2K\r")
}

func TestStartupWriter_GoroutineExitsOnReaderClose(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	pr, pw := io.Pipe()

	sw := newStartupWriter(&buf, &status, 5*time.Second, pr, func() {})

	_ = pw.Close()

	select {
	case <-sw.logDone:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not exit after reader was closed")
	}
}

func TestStartupWriter_GoroutineExitsOnActivate(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	cancelled := make(chan struct{})
	cancelFn := func() {
		close(cancelled)
		_ = pw.Close()
	}

	sw := newStartupWriter(&buf, &status, 5*time.Second, pr, cancelFn)

	_, err := sw.Write([]byte("\x1b[?1049h" + strings.Repeat("x", activateMinVisible)))
	require.NoError(t, err)

	select {
	case <-sw.logDone:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not exit after activate")
	}
}

func TestStartupWriter_NoStatusWritesAfterActivate(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	pr, pw := io.Pipe()

	cancelled := make(chan struct{})
	cancelFn := func() {
		close(cancelled)
		_ = pw.Close()
	}

	sw := newStartupWriter(&buf, &status, 5*time.Second, pr, cancelFn)

	go func() {
		_, _ = pw.Write([]byte("level=INFO msg=\"first line\"\n"))
		for i := 0; i < 10; i++ {
			select {
			case <-cancelled:
				return
			case <-time.After(10 * time.Millisecond):
			}
			_, _ = pw.Write([]byte("level=INFO msg=\"more data\"\n"))
		}
	}()

	time.Sleep(150 * time.Millisecond)

	_, err := sw.Write([]byte("\x1b[?1049h" + strings.Repeat("x", activateMinVisible)))
	require.NoError(t, err)

	select {
	case <-sw.logDone:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine did not exit")
	}

	statusLenAfterActivate := status.Len()

	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, statusLenAfterActivate, status.Len(), "status should not grow after activate")
}

func TestExtractLogfmtMsg_UnquotedValue(t *testing.T) {
	t.Parallel()

	result := extractLogfmtMsg("level=INFO msg=connecting")
	assert.Equal(t, "connecting", result)
}

func TestExtractLogfmtMsg_QuotedWithSpaces(t *testing.T) {
	t.Parallel()

	result := extractLogfmtMsg(`level=INFO msg="LSP is ready" component=x`)
	assert.Equal(t, "LSP is ready", result)
}

func TestExtractLogfmtMsg_NoMsgField(t *testing.T) {
	t.Parallel()

	result := extractLogfmtMsg("Setting up iptables")
	assert.Equal(t, "", result)
}

func TestExtractLogfmtMsg_EmptyMsg(t *testing.T) {
	t.Parallel()

	result := extractLogfmtMsg(`msg=""`)
	assert.Equal(t, "", result)
}

func TestExtractLogfmtMsg_MsgAtEnd(t *testing.T) {
	t.Parallel()

	result := extractLogfmtMsg("level=INFO msg=done")
	assert.Equal(t, "done", result)
}
