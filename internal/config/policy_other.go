//go:build !unix

package config

func defaultPolicyPaths() []string { return nil }

func verifyPolicyTrust(string) error { return nil }
