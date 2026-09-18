//go:build !windows

package sandbox

func repairLegacyCredentialDeny(string) error { return nil }
