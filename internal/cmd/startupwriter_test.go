package cmd

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartupWriter_PassthroughWhenNilStatus(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	sw := newStartupWriter(&buf, nil, time.Second)

	n, err := sw.Write([]byte("hello"))
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "hello", buf.String())
}

func TestStartupWriter_BuffersUntilAltScreen(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	sw := newStartupWriter(&buf, &status, 5*time.Second)

	// Write data without alt-screen escape — should be buffered.
	_, err := sw.Write([]byte("buffered data"))
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "data should be buffered, not written to underlying writer")

	// Write alt-screen enable + content — should flush everything.
	_, err = sw.Write([]byte("\x1b[?1049h\x1b[H"))
	require.NoError(t, err)
	assert.Equal(t, "buffered data\x1b[?1049h\x1b[H", buf.String())
}

func TestStartupWriter_CrossBoundaryDetection(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	sw := newStartupWriter(&buf, &status, 5*time.Second)

	// First write ends mid-sequence: \x1b[?10 (5 of 8 bytes).
	_, err := sw.Write([]byte("somedata\x1b[?10"))
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "should still be buffering")

	// Second write completes the sequence: 49h + more content.
	_, err = sw.Write([]byte("49h\x1b[H\x1b[2J"))
	require.NoError(t, err)
	assert.Equal(t, "somedata\x1b[?1049h\x1b[H\x1b[2J", buf.String())
}

func TestStartupWriter_TimeoutFlushes(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	sw := newStartupWriter(&buf, &status, 50*time.Millisecond)

	_, err := sw.Write([]byte("waiting for timeout"))
	require.NoError(t, err)
	assert.Empty(t, buf.String(), "should be buffered before timeout")

	time.Sleep(150 * time.Millisecond)

	assert.Equal(t, "waiting for timeout", buf.String())
}

func TestStartupWriter_CloseFlushesBuffer(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	sw := newStartupWriter(&buf, &status, 5*time.Second)

	_, err := sw.Write([]byte("pending data"))
	require.NoError(t, err)
	assert.Empty(t, buf.String())

	err = sw.Close()
	require.NoError(t, err)
	assert.Equal(t, "pending data", buf.String())
}

func TestStartupWriter_ReadyPassthrough(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var status bytes.Buffer
	sw := newStartupWriter(&buf, &status, 5*time.Second)

	// Trigger detection with alt-screen sequence.
	_, err := sw.Write([]byte("\x1b[?1049h"))
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
	sw := newStartupWriter(&buf, &status, 5*time.Second)

	// Status should have the loading message from constructor.
	assert.Equal(t, "Starting OpenCode...\r\n", status.String())

	// Trigger activate via Close.
	err := sw.Close()
	require.NoError(t, err)

	// Status should now also have the erase sequence.
	assert.Equal(t, "Starting OpenCode...\r\n\x1b[2K\r", status.String())
}
