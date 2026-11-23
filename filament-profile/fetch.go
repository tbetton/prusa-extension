package main

// Fetching the Prusa Research profile archive and picking the newest bundle
// out of it. The archive holds one INI per released version, about 14 MiB in
// all. Only the highest release is read and kept.

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// maxArchive bounds the download, and maxBundle the one file read from it.
	maxArchive = 64 << 20
	maxBundle  = 32 << 20

	bundleFile = "PrusaResearch.ini"
)

// version is a release such as 2.5.16, ordered number by number.
type version []int

func parseVersion(s string) (version, bool) {
	var v version

	for _, part := range strings.Split(s, ".") {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return nil, false
		}

		v = append(v, n)
	}

	return v, len(v) > 0
}

func (v version) newerThan(o version) bool {
	for i := range max(len(v), len(o)) {
		a, b := 0, 0
		if i < len(v) {
			a = v[i]
		}

		if i < len(o) {
			b = o[i]
		}

		if a != b {
			return a > b
		}
	}

	return false
}

var errNoBundle = errors.New("the archive holds no PrusaResearch/<version>.ini")

// newestBundle reads the highest released bundle of the archive, skipping
// alpha and beta files.
func newestBundle(archive []byte) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return nil, fmt.Errorf("read the archive: %w", err)
	}

	var (
		best version
		file *zip.File
	)

	for _, f := range zr.File {
		dir, base := path.Split(f.Name)
		if dir != "PrusaResearch/" || !strings.HasSuffix(base, ".ini") {
			continue
		}

		v, ok := parseVersion(strings.TrimSuffix(base, ".ini"))
		if !ok || (file != nil && !v.newerThan(best)) {
			continue
		}

		best, file = v, f
	}

	if file == nil {
		return nil, errNoBundle
	}

	if file.UncompressedSize64 > maxBundle {
		return nil, fmt.Errorf("%s is larger than %d bytes", file.Name, maxBundle)
	}

	rc, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", file.Name, err)
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, maxBundle+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file.Name, err)
	}

	if len(data) > maxBundle {
		return nil, fmt.Errorf("%s is larger than %d bytes", file.Name, maxBundle)
	}

	return data, nil
}

// download fetches the archive from the address the operator configured.
func download(ctx context.Context, client *http.Client, address string) ([]byte, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, fmt.Errorf("catalogue_url %q is not an https address", address)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build the request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download the catalogue: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download the catalogue: %s", resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArchive+1))
	if err != nil {
		return nil, fmt.Errorf("download the catalogue: %w", err)
	}

	if len(data) > maxArchive {
		return nil, fmt.Errorf("the catalogue is larger than %d bytes", maxArchive)
	}

	return data, nil
}

// saveBundle keeps the bundle in the data directory, through a temporary file
// so a crash never leaves half a catalogue.
func saveBundle(dir string, data []byte) error {
	tmp, err := os.CreateTemp(dir, bundleFile+".*")
	if err != nil {
		return fmt.Errorf("save the catalogue: %w", err)
	}

	defer os.Remove(tmp.Name())

	if _, err = tmp.Write(data); err != nil {
		tmp.Close()

		return fmt.Errorf("save the catalogue: %w", err)
	}

	if err = tmp.Close(); err != nil {
		return fmt.Errorf("save the catalogue: %w", err)
	}

	if err = os.Rename(tmp.Name(), filepath.Join(dir, bundleFile)); err != nil {
		return fmt.Errorf("save the catalogue: %w", err)
	}

	return nil
}
