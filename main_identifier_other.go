//go:build !windows && !darwin

package main

// dropLegacyInstallEntry is for the Windows installer's registry entry.
func dropLegacyInstallEntry() {}

// dropLegacyLoginItem is for the login items MyGo keeps by identifier.
func dropLegacyLoginItem() {}
