package main

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func archive(t *testing.T, files map[string]string) []byte {
	t.Helper()

	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)

	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}

		if _, err = w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	return buf.Bytes()
}

func TestNewestBundleTakesTheHighestRelease(t *testing.T) {
	data, err := newestBundle(archive(t, map[string]string{
		"PrusaResearch/2.5.9.ini":         "nine",
		"PrusaResearch/2.5.16.ini":        "sixteen",
		"PrusaResearch/2.5.17-alpha1.ini": "alpha",
		"PrusaResearch/1.14.1.ini":        "old",
		"PrusaResearch/coreone.svg":       "svg",
		"Other/9.9.9.ini":                 "elsewhere",
	}))
	if err != nil {
		t.Fatal(err)
	}

	if string(data) != "sixteen" {
		t.Fatalf("bundle = %q, want the 2.5.16 one", data)
	}
}

func TestNewestBundleRefusesAnArchiveWithoutOne(t *testing.T) {
	if _, err := newestBundle(archive(t, map[string]string{"PrusaResearch/readme.txt": "x"})); err == nil {
		t.Fatal("an archive with no bundle was read")
	}

	if _, err := newestBundle([]byte("not a zip")); err == nil {
		t.Fatal("garbage was read")
	}
}

func TestVersionOrdersNumbersNotText(t *testing.T) {
	a, _ := parseVersion("2.5.16")
	b, _ := parseVersion("2.5.9")
	c, _ := parseVersion("2.5")

	if !a.newerThan(b) || b.newerThan(a) || !b.newerThan(c) || c.newerThan(c) {
		t.Fatal("versions are ordered as text")
	}

	if _, ok := parseVersion("2.5.0-alpha"); ok {
		t.Fatal("a pre-release was accepted")
	}
}

func TestDownloadRefusesWhatIsNotHTTPS(t *testing.T) {
	if _, err := download(context.Background(), http.DefaultClient, "http://example.com/a.zip"); err == nil {
		t.Fatal("a plain http address was fetched")
	}

	if _, err := download(context.Background(), http.DefaultClient, "file:///etc/passwd"); err == nil {
		t.Fatal("a file address was fetched")
	}
}

func TestDownloadReadsAnArchiveAndRefusesAnErrorStatus(t *testing.T) {
	good := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("zip bytes"))
	}))
	defer good.Close()

	data, err := download(context.Background(), good.Client(), good.URL)
	if err != nil || string(data) != "zip bytes" {
		t.Fatalf("download = %q, %v", data, err)
	}

	bad := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	defer bad.Close()

	if _, err = download(context.Background(), bad.Client(), bad.URL); err == nil {
		t.Fatal("a 404 was read as an archive")
	}
}

func TestSaveBundleReplacesTheFile(t *testing.T) {
	dir := t.TempDir()

	for _, text := range []string{"first", "second"} {
		if err := saveBundle(dir, []byte(text)); err != nil {
			t.Fatal(err)
		}
	}

	got, err := os.ReadFile(filepath.Join(dir, bundleFile))
	if err != nil || string(got) != "second" {
		t.Fatalf("bundle = %q, %v", got, err)
	}

	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("a temporary file was left: %v", entries)
	}
}
