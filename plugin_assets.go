package main

import "embed"

// pluginAssets is every agent plugin afk ships, embedded so `afk plugin
// install` writes the plugin version this binary was built with. The root
// marketplace manifests are included so the extracted tree is a marketplace
// for both Claude Code (.claude-plugin) and Codex (.agents). `all:` keeps the
// dot-directories inside plugins/ (.claude-plugin, .codex-plugin).
//
//go:embed .claude-plugin .agents all:plugins
var pluginAssets embed.FS
