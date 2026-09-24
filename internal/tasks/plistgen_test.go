package tasks

import (
	"strings"
	"testing"
)

// TestBuildLaunchdPlist_Structure launchd plist 结构断言：Label com.ddns-hosts-sync、
// ProgramArguments=[ExecPath, sync]、RunAtLoad=true、StartInterval=60、XML 声明。
func TestBuildLaunchdPlist_Structure(t *testing.T) {
	execPath := `/usr/local/bin/ddns-hosts-sync`
	spec := TaskSpec{Name: "ddns-hosts-sync", ExecPath: execPath, Args: []string{"sync"}}
	out, err := BuildLaunchdPlist(spec)
	if err != nil {
		t.Fatalf("BuildLaunchdPlist: %v", err)
	}
	text := string(out)
	for _, want := range []string{
		`<?xml version="1.0" encoding="UTF-8"?>`,
		`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"`,
		`<key>Label</key>`,
		`<string>com.ddns-hosts-sync</string>`,
		`<key>ProgramArguments</key>`,
		`<string>/usr/local/bin/ddns-hosts-sync</string>`,
		`<string>sync</string>`,
		`<key>RunAtLoad</key>`,
		`<true/>`,
		`<key>StartInterval</key>`,
		`<integer>60</integer>`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plist 缺期望片段 %q:\n%s", want, text)
		}
	}
	if i := strings.Index(text, "<array>"); i < 0 || strings.Index(text, "</array>") < i {
		t.Errorf("ProgramArguments 应为 array 结构:\n%s", text)
	}
}

// TestBuildLaunchdPlist_EscapesSpecialChars ExecPath 含 &<> 必须转义为实体
// （launchd 按 XML 解析 plist，未转义会破坏结构）。
func TestBuildLaunchdPlist_EscapesSpecialChars(t *testing.T) {
	spec := TaskSpec{Name: "ddns-hosts-sync", ExecPath: `/opt/dir&<x>ddns`, Args: []string{"sync"}}
	out, err := BuildLaunchdPlist(spec)
	if err != nil {
		t.Fatalf("BuildLaunchdPlist: %v", err)
	}
	text := string(out)
	for _, esc := range []string{"&amp;", "&lt;", "&gt;"} {
		if !strings.Contains(text, esc) {
			t.Errorf("plist 应含转义序列 %q:\n%s", esc, text)
		}
	}
	if strings.Contains(text, "dir&<x>") {
		t.Errorf("plist 不得含未转义元字符:\n%s", text)
	}
}
