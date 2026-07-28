package strm

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const testSHA1 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestWriteCreatesRealSTRM(t *testing.T) {
	root := t.TempDir()
	service, err := New(root, "https://media.example.test/base/")
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.Write(
		context.Background(), "Movie/video.mkv.strm", testSHA1, "",
	)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content),
		"https://media.example.test/base/redirect/"+testSHA1+"\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestReplacePrefixUpdatesOnlyMatchingSTRM(t *testing.T) {
	root := t.TempDir()
	managed := filepath.Join(root, "managed.strm")
	foreign := filepath.Join(root, "foreign.strm")
	if err := os.WriteFile(
		managed, []byte("http://old.test/redirect/"+testSHA1+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(foreign, []byte("https://videos.test/movie.mkv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service, err := New(root, "https://new.test")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ReplacePrefix(
		context.Background(),
		"http://old.test/redirect",
		"https://new.test/redirect",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 2 || result.Rewritten != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
	content, _ := os.ReadFile(managed)
	if got, want := string(content), "https://new.test/redirect/"+testSHA1+"\n"; got != want {
		t.Fatalf("managed content = %q, want %q", got, want)
	}
	content, _ = os.ReadFile(foreign)
	if got := string(content); got != "https://videos.test/movie.mkv\n" {
		t.Fatalf("foreign STRM was changed: %q", got)
	}
}

func TestReplacePrefixRequiresPathBoundary(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "foreign.strm")
	if err := os.WriteFile(
		target, []byte("http://old.test/redirect-other/"+testSHA1+"\n"), 0o644,
	); err != nil {
		t.Fatal(err)
	}
	service, err := New(root, "https://media.test")
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ReplacePrefix(
		context.Background(),
		"http://old.test/redirect",
		"http://new.test/redirect",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Rewritten != 0 {
		t.Fatalf("unexpected rewrite: %+v", result)
	}
}

func TestPathRejectsTraversal(t *testing.T) {
	service, err := New(t.TempDir(), "https://media.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Path("../escape.strm"); err == nil {
		t.Fatal("expected traversal to be rejected")
	}
}

func TestWriteRefusesForeignSTRM(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "video.strm")
	if err := os.WriteFile(target, []byte("https://videos.test/movie.mkv\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service, err := New(root, "https://media.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Write(
		context.Background(), "video.strm", testSHA1, "",
	); err == nil {
		t.Fatal("expected foreign STRM overwrite to fail")
	}
}

func TestWriteReplacesManagedSTRMWhenContentChanges(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "video.mkv.strm")
	if err := os.WriteFile(
		target,
		[]byte("https://old.test/redirect/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	service, err := New(root, "https://media.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Write(
		context.Background(), "video.mkv.strm", testSHA1, "",
	); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(content),
		"https://media.test/redirect/"+testSHA1+"\n"; got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func TestWriteRejectsSymlinkedSubdirectory(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	service, err := New(root, "https://media.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Write(
		context.Background(), "linked/video.strm", testSHA1, "",
	); err == nil {
		t.Fatal("expected symlinked output directory to be rejected")
	}
	if _, err := os.Stat(filepath.Join(outside, "video.strm")); !os.IsNotExist(err) {
		t.Fatalf("file escaped through symlink: %v", err)
	}
}

func TestWriteIncludesSourceInfoHash(t *testing.T) {
	const infoHash = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	service, err := New(t.TempDir(), "https://media.test")
	if err != nil {
		t.Fatal(err)
	}
	target, err := service.Write(
		context.Background(), "video.mkv.strm", testSHA1, infoHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	want := "https://media.test/redirect/" + testSHA1 +
		"?info_hash=" + infoHash + "\n"
	if got := string(content); got != want {
		t.Fatalf("content = %q, want %q", got, want)
	}
	if _, managed := redirectSHA1(string(content)); !managed {
		t.Fatal("STRM with source info hash was not recognized as managed")
	}
}
