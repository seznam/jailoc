package embed_test

import (
	"strings"
	"testing"

	jailocembed "github.com/seznam/jailoc/internal/embed"
)

func TestDockerfileEmbedded(t *testing.T) {
	t.Parallel()
	if !strings.Contains(string(jailocembed.Dockerfile()), "FROM") {
		t.Fatal("Dockerfile() does not contain FROM")
	}
}

func TestDockerLayerEmbedded(t *testing.T) {
	t.Parallel()
	if !strings.Contains(string(jailocembed.DockerLayer()), "AS docker") {
		t.Fatal("DockerLayer() does not contain the docker stage")
	}
}

func TestDesktopLayerEmbedded(t *testing.T) {
	t.Parallel()
	layer := string(jailocembed.DesktopLayer())
	for _, want := range []string{
		"tigervnc-standalone-server", "novnc", "websockify", "openbox", "dbus-x11",
		"xterm", "fonts-dejavu-core", "x11-utils", "xauth", "apt-get", "Debian or Ubuntu",
	} {
		if !strings.Contains(layer, want) {
			t.Errorf("DesktopLayer() does not contain %q", want)
		}
	}
	for _, unwanted := range []string{"firefox", "chromium", "snap", "xvfb"} {
		if strings.Contains(strings.ToLower(layer), unwanted) {
			t.Errorf("DesktopLayer() contains %q", unwanted)
		}
	}
	if strings.Contains(layer, "FROM") {
		t.Error("DesktopLayer() must be a RUN fragment without FROM")
	}
}

func TestTUIPluginEmbedded(t *testing.T) {
	t.Parallel()
	if len(jailocembed.TUIPluginOpenCode1JS()) == 0 || len(jailocembed.TUIPluginOpenCode2JS()) == 0 || len(jailocembed.TUIPluginJSON()) == 0 {
		t.Fatal("TUI plugin assets are empty")
	}
}
