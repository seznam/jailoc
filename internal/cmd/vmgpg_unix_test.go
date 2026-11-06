//go:build darwin || linux

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSecretKeyFingerprints(t *testing.T) {
	t.Parallel()

	list := `sec:u:255:22:AAAA1111AAAA1111:1700000000:::u:::scESC:::+:::ed25519:::0:
fpr:::::::::AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1111:
grp:::::::::0123456789ABCDEF0123456789ABCDEF01234567:
uid:u::::1700000000::HASH::Me <me@example.com>::::::::::0:
ssb:u:255:18:BBBB2222BBBB2222:1700000000::::::e:::+:::cv25519::
fpr:::::::::BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB2222:
grp:::::::::89ABCDEF0123456789ABCDEF0123456789ABCDEF:
sec:e:3072:1:CCCC3333CCCC3333:1600000000:1650000000::u:::sc:::+:::::0:
fpr:::::::::CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC3333:
`
	got := secretKeyFingerprints([]byte(list))
	want := []string{
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA1111",
		"CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC3333",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("secretKeyFingerprints = %v, want %v", got, want)
	}
	if got := secretKeyFingerprints(nil); len(got) != 0 {
		t.Fatalf("secretKeyFingerprints(nil) = %v, want none", got)
	}
}

func TestGitSigningKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	cfg := filepath.Join(home, ".gitconfig")

	if got := gitSigningKey(context.Background()); got != "" {
		t.Fatalf("gitSigningKey with no config = %q, want empty", got)
	}
	if err := os.WriteFile(cfg, []byte("[user]\n\tsigningKey = DEADBEEF\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := gitSigningKey(context.Background()); got != "DEADBEEF" {
		t.Fatalf("gitSigningKey = %q, want DEADBEEF", got)
	}
	if err := os.WriteFile(cfg, []byte("[gpg]\n\tformat = ssh\n[user]\n\tsigningKey = ~/.ssh/id_ed25519.pub\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := gitSigningKey(context.Background()); got != "" {
		t.Fatalf("gitSigningKey with gpg.format=ssh = %q, want empty", got)
	}
}
