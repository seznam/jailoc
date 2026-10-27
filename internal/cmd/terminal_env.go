package cmd

import "os"

func terminalEnv() []string {
	var env []string
	for _, key := range []string{"TERM", "COLORTERM", "CLICOLOR_FORCE", "TERM_PROGRAM", "COLORFGBG", "LANG"} {
		if value := os.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	if os.Getenv("CLICOLOR_FORCE") == "" {
		env = append(env, "CLICOLOR_FORCE=1")
	}
	if os.Getenv("COLORFGBG") == "" {
		env = append(env, "COLORFGBG=15;0")
	}
	if os.Getenv("COLORTERM") == "" {
		env = append(env, "COLORTERM=truecolor")
	}
	if os.Getenv("LANG") == "" {
		env = append(env, "LANG=C.UTF-8")
	}
	return env
}
