//go:build !windows

package main

func lockACLSystemAdminsOnly(path string) error { return nil }
