package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSBDCLIQueueAndLoopback(t *testing.T) {
	dir := t.TempDir()
	result := runInProcess(t, world{}, "--simulate=instant", "sbd", "--queue-dir", dir, "loopback", "--text", "hello")
	if !strings.Contains(result.stdout, "\"matched\":true") {
		t.Fatal(result)
	}
	result = runInProcess(t, world{}, "sbd", "--queue-dir", dir, "enqueue", "--text", "hello")
	id := strings.TrimSpace(result.stdout)
	if len(id) != 32 {
		t.Fatal(result)
	}
	result = runInProcess(t, world{}, "sbd", "--queue-dir", dir, "outbox")
	var records []struct {
		ID      string
		Payload []byte
	}
	if e := json.Unmarshal([]byte(result.stdout), &records); e != nil || len(records) != 1 || records[0].ID != id || string(records[0].Payload) != "hello" {
		t.Fatal(result, e)
	}
}
func TestSBDCLIBinaryPayloadAndArgumentValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.bin")
	p := []byte{0, 13, 10, 255, 1}
	if e := os.WriteFile(path, p, 0600); e != nil {
		t.Fatal(e)
	}
	r := runInProcess(t, world{}, "sbd", "--queue-dir", dir, "enqueue", "--file", path)
	if len(strings.TrimSpace(r.stdout)) != 32 {
		t.Fatal(r)
	}
	for _, args := range [][]string{{"send"}, {"enqueue", "--text", "hi", "--file", path}, {"enqueue", "--text", ""}} {
		r = runInProcess(t, world{}, append([]string{"sbd", "--queue-dir", dir}, args...)...)
		if !strings.Contains(r.stderr, "error:") {
			t.Fatal(r)
		}
	}
}
