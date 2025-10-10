// Copyright 2025 getDragon-dev
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

type Blueprint struct {
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Repo        string   `json:"repo"`
	Path        string   `json:"path"`
	DownloadURL string   `json:"download_url"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

type Database struct {
	Blueprints []Blueprint `json:"blueprints"`
}

func loadDB(p string) (Database, error) {
	var db Database
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Database{Blueprints: []Blueprint{}}, nil
		}
		return db, err
	}
	if err := json.Unmarshal(b, &db); err != nil {
		return db, err
	}
	// ensure non-nil slice to avoid "null"
	if db.Blueprints == nil {
		db.Blueprints = []Blueprint{}
	}
	return db, nil
}

func saveDB(p string, db Database) error {
	if db.Blueprints == nil {
		db.Blueprints = []Blueprint{}
	}
	b, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

type bpManifest struct {
	Name        string   `yaml:"name"`
	Version     string   `yaml:"version"`
	Description string   `yaml:"description"`
	Tags        []string `yaml:"tags"`
}

func httpGet(ctx context.Context, url string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET %s: %d: %s", url, resp.StatusCode, string(b))
	}
	return io.ReadAll(resp.Body)
}

func main() {
	ctx := context.Background()
	tag := os.Getenv("TAG")
	repo := os.Getenv("BLUEPRINTS_REPO") // e.g. getDragon-dev/dragon-blueprints
	if tag == "" || repo == "" {
		fmt.Fprintln(os.Stderr, "missing TAG or BLUEPRINTS_REPO env")
		os.Exit(1)
	}

	db, err := loadDB("registry.json")
	if err != nil {
		fmt.Fprintln(os.Stderr, "load registry:", err)
		os.Exit(1)
	}

	// Fetch release metadata for this tag
	relURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", repo, tag)
	rb, err := httpGet(ctx, relURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
	var rel ghRelease
	if err := json.Unmarshal(rb, &rel); err != nil {
		fmt.Fprintln(os.Stderr, "decode release:", err)
		os.Exit(1)
	}

	// Iterate assets like "<name>.zip"
	for _, a := range rel.Assets {
		if !strings.HasSuffix(a.Name, ".zip") {
			continue
		}
		name := strings.TrimSuffix(a.Name, ".zip")
		// Fetch manifest.yaml from the repo at this tag
		manifestURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s/%s",
			repo, tag, path.Join("blueprints", name, "manifest.yaml"), "")
		mb, err := httpGet(ctx, manifestURL)
		var man bpManifest
		if err == nil {
			_ = yaml.Unmarshal(mb, &man)
		}
		// Fallbacks if manifest missing
		if man.Name == "" {
			man.Name = name
		}
		if man.Version == "" {
			man.Version = strings.TrimPrefix(tag, "v")
		}
		if man.Description == "" {
			man.Description = fmt.Sprintf("%s blueprint", name)
		}

		entry := Blueprint{
			Name:        man.Name,
			Version:     man.Version,
			Repo:        "github.com/" + repo,
			Path:        path.Join("blueprints", name),
			DownloadURL: a.BrowserDownloadURL,
			Description: man.Description,
			Tags:        man.Tags,
		}

		// Upsert into db
		found := false
		for i := range db.Blueprints {
			if db.Blueprints[i].Name == entry.Name {
				db.Blueprints[i] = entry
				found = true
				break
			}
		}
		if !found {
			db.Blueprints = append(db.Blueprints, entry)
		}
	}

	if err := saveDB("registry.json", db); err != nil {
		fmt.Fprintln(os.Stderr, "save registry:", err)
		os.Exit(1)
	}

	fmt.Printf("registry updated for %s at %s with %d entries\n", tag, time.Now().Format(time.RFC3339), len(db.Blueprints))
}