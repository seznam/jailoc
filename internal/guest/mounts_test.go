package guest

import (
	"slices"
	"testing"
)

func TestMountOrder(t *testing.T) {
	t.Parallel()
	shares := []Share{
		{Tag: "skills", GuestPath: "/home/agent/.claude/skills"},
		{Tag: "project", GuestPath: "/home/agent/src/project"},
		{Tag: "local", GuestPath: "/home/agent/.local"},
		{Tag: "internal", GuestPath: HostDir},
	}
	binds := []Bind{
		{Source: HostDir + "/config", Target: "/etc/jailoc"},
		{Source: HostDir + "/home/claude", Target: "/home/agent/.claude"},
		{Source: HostDir + "/home/opencode-data", Target: "/home/agent/.local/share/opencode"},
	}
	var got []string
	for _, m := range mountOrder(shares, binds) {
		kind := "bind"
		if m.share != nil {
			kind = "share"
		}
		got = append(got, kind+" "+m.target)
	}
	want := []string{
		"share " + HostDir,
		"bind /etc/jailoc",
		"share /home/agent/.local",
		"bind /home/agent/.claude",
		"share /home/agent/.claude/skills",
		"share /home/agent/src/project",
		"bind /home/agent/.local/share/opencode",
	}
	if !slices.Equal(got, want) {
		t.Errorf("mountOrder() =\n%q\nwant\n%q", got, want)
	}
}
