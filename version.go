package main

// version is set at build time via -ldflags "-X main.version=<value>".
// Default to "dev" when not provided.
var version = "dev"
