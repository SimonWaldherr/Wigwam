package main

import "testing"

func TestParseRange(t *testing.T) {
    // bytes=0-9 on size 100 -> [0,10)
    s, e, ok := parseRange("bytes=0-9", 100)
    if !ok || s != 0 || e != 10 { t.Fatalf("got %v %d-%d", ok, s, e) }

    // suffix: last 10 bytes
    s, e, ok = parseRange("bytes=-10", 100)
    if !ok || s != 90 || e != 100 { t.Fatalf("suffix got %v %d-%d", ok, s, e) }

    // open ended
    s, e, ok = parseRange("bytes=50-", 100)
    if !ok || s != 50 || e != 100 { t.Fatalf("open got %v %d-%d", ok, s, e) }

    // invalid
    if _, _, ok = parseRange("bytes=200-300", 100); ok { t.Fatal("expected invalid range") }
}

func TestMimeByExt(t *testing.T) {
    if ct := mimeByExt("file.css"); ct == "application/octet-stream" { t.Fatal("css should not be octet-stream") }
    if ct := mimeByExt("file.wasm"); ct != "application/wasm" { t.Fatalf("wasm ct: %s", ct) }
}
