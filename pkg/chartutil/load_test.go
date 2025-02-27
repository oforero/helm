/*
Copyright The Helm Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package chartutil

import (
	"archive/tar"
	"compress/gzip"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ghodss/yaml"
	"k8s.io/helm/pkg/proto/hapi/chart"
)

func TestLoadDir(t *testing.T) {
	c, err := Load("testdata/frobnitz")
	if err != nil {
		t.Fatalf("Failed to load testdata: %s", err)
	}
	verifyFrobnitz(t, c)
	verifyChart(t, c)
	verifyRequirements(t, c)
}

func TestLoadDirWithEnvValuesFile(t *testing.T) {
	expectedDev1 := Values{
		"albatross": "true",
		"env":       "dev1",
		"global": map[string]interface{}{
			"author": "Coleridge",
		},
	}

	expectedDev2 := Values{
		"albatross": "true",
		"env":       "dev2",
		"global": map[string]interface{}{
			"author": "Coleridge",
		},
	}

	dev1, err := LoadWithEnvValuesFile("testdata/albatross", "dev1.yaml")
	if err != nil {
		t.Fatalf("Failed to load testdata: %s", err)
	}
	dev1values := Values{}
	yaml.Unmarshal([]byte(dev1.Values.Raw), &dev1values)
	equal := reflect.DeepEqual(expectedDev1, dev1values)
	if !equal {
		t.Errorf("Expected chart values to be populated with default values. Expected: %v, got %v", expectedDev1, dev1values)
	}

	dev2, err := LoadWithEnvValuesFile("testdata/albatross", "dev2.yaml")
	if err != nil {
		t.Fatalf("Failed to load testdata: %s", err)
	}
	dev2values := Values{}
	yaml.Unmarshal([]byte(dev2.Values.Raw), &dev2values)
	equal = reflect.DeepEqual(expectedDev2, dev2values)
	if !equal {
		t.Errorf("Expected chart values to be populated with default values. Expected: %v, got %v", expectedDev2, dev2values)
	}

}

func TestLoadNonV1Chart(t *testing.T) {
	_, err := Load("testdata/frobnitz.v2")
	if err != nil {
		if strings.Compare(err.Error(), "apiVersion 'v2' is not valid. The value must be \"v1\"") != 0 {
			t.Errorf("Unexpected message: %s", err)
		}
		return
	}
	t.Fatalf("chart with v2 apiVersion should not load")
}

func TestLoadFile(t *testing.T) {
	c, err := Load("testdata/frobnitz-1.2.3.tgz")
	if err != nil {
		t.Fatalf("Failed to load testdata: %s", err)
	}
	verifyFrobnitz(t, c)
	verifyChart(t, c)
	verifyRequirements(t, c)
}

func TestLoadArchive_InvalidArchive(t *testing.T) {
	tmpdir, err := ioutil.TempDir("", "helm-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(tmpdir)

	writeTar := func(filename, internalPath string, body []byte) {
		dest, err := os.Create(filename)
		if err != nil {
			t.Fatal(err)
		}
		zipper := gzip.NewWriter(dest)
		tw := tar.NewWriter(zipper)

		h := &tar.Header{
			Name:    internalPath,
			Mode:    0755,
			Size:    int64(len(body)),
			ModTime: time.Now(),
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
		tw.Close()
		zipper.Close()
		dest.Close()
	}

	for _, tt := range []struct {
		chartname   string
		internal    string
		expectError string
	}{
		{"illegal-dots.tgz", "../../malformed-helm-test", "chart illegally references parent directory"},
		{"illegal-dots2.tgz", "/foo/../../malformed-helm-test", "chart illegally references parent directory"},
		{"illegal-dots3.tgz", "/../../malformed-helm-test", "chart illegally references parent directory"},
		{"illegal-dots4.tgz", "./../../malformed-helm-test", "chart illegally references parent directory"},
		{"illegal-name.tgz", "./.", "chart illegally contains content outside the base directory: \"./.\""},
		{"illegal-name2.tgz", "/./.", "chart illegally contains content outside the base directory: \"/./.\""},
		{"illegal-name3.tgz", "missing-leading-slash", "chart illegally contains content outside the base directory: \"missing-leading-slash\""},
		{"illegal-name5.tgz", "content-outside-base-dir", "chart illegally contains content outside the base directory: \"content-outside-base-dir\""},
		{"illegal-name4.tgz", "/missing-leading-slash", "chart metadata (Chart.yaml) missing"},
		{"illegal-abspath.tgz", "//foo", "chart illegally contains absolute paths"},
		{"illegal-abspath2.tgz", "///foo", "chart illegally contains absolute paths"},
		{"illegal-abspath3.tgz", "\\\\foo", "chart illegally contains absolute paths"},
		{"illegal-abspath3.tgz", "\\..\\..\\foo", "chart illegally references parent directory"},

		// Under special circumstances, this can get normalized to things that look like absolute Windows paths
		{"illegal-abspath4.tgz", "\\.\\c:\\\\foo", "chart contains illegally named files"},
		{"illegal-abspath5.tgz", "/./c://foo", "chart contains illegally named files"},
		{"illegal-abspath6.tgz", "\\\\?\\Some\\windows\\magic", "chart illegally contains absolute paths"},
	} {
		illegalChart := filepath.Join(tmpdir, tt.chartname)
		writeTar(illegalChart, tt.internal, []byte("hello: world"))
		_, err = Load(illegalChart)
		if err == nil {
			t.Fatal("expected error when unpacking illegal files")
		}
		if err.Error() != tt.expectError {
			t.Errorf("Expected %q, got %q for %s", tt.expectError, err.Error(), tt.chartname)
		}
	}

	// Make sure that absolute path gets interpreted as relative
	illegalChart := filepath.Join(tmpdir, "abs-path.tgz")
	writeTar(illegalChart, "/Chart.yaml", []byte("hello: world"))
	_, err = Load(illegalChart)
	if err.Error() != "invalid chart (Chart.yaml): name must not be empty" {
		t.Error(err)
	}

	// And just to validate that the above was not spurious
	illegalChart = filepath.Join(tmpdir, "abs-path2.tgz")
	writeTar(illegalChart, "files/whatever.yaml", []byte("hello: world"))
	_, err = Load(illegalChart)
	if err.Error() != "chart metadata (Chart.yaml) missing" {
		t.Error(err)
	}

	// Finally, test that drive letter gets stripped off on Windows
	illegalChart = filepath.Join(tmpdir, "abs-winpath.tgz")
	writeTar(illegalChart, "c:\\Chart.yaml", []byte("hello: world"))
	_, err = Load(illegalChart)
	if err.Error() != "invalid chart (Chart.yaml): name must not be empty" {
		t.Error(err)
	}
}

func TestLoadFiles(t *testing.T) {
	goodFiles := []*BufferedFile{
		{
			Name: ChartfileName,
			Data: []byte(`apiVersion: v1
name: frobnitz
description: This is a frobnitz.
version: "1.2.3"
keywords:
  - frobnitz
  - sprocket
  - dodad
maintainers:
  - name: The Helm Team
    email: helm@example.com
  - name: Someone Else
    email: nobody@example.com
sources:
  - https://example.com/foo/bar
home: http://example.com
icon: https://example.com/64x64.png
`),
		},
		{
			Name: ValuesfileName,
			Data: []byte(defaultValues),
		},
		{
			Name: path.Join("templates", DeploymentName),
			Data: []byte(defaultDeployment),
		},
		{
			Name: path.Join("templates", ServiceName),
			Data: []byte(defaultService),
		},
	}

	c, err := LoadFiles(goodFiles)
	if err != nil {
		t.Errorf("Expected good files to be loaded, got %v", err)
	}

	if c.Metadata.Name != "frobnitz" {
		t.Errorf("Expected chart name to be 'frobnitz', got %s", c.Metadata.Name)
	}

	values := Values{}
	yaml.Unmarshal([]byte(c.Values.Raw), &values)
	expectedValues := Values{}
	yaml.Unmarshal([]byte(c.Values.Raw), &expectedValues)
	equal := reflect.DeepEqual(values, expectedValues)
	if !equal {
		t.Errorf("Expected chart values to be populated with default values. Expected: %v, got %v", values, expectedValues)
	}

	if len(c.Templates) != 2 {
		t.Errorf("Expected number of templates == 2, got %d", len(c.Templates))
	}

	c, err = LoadFiles([]*BufferedFile{})
	if err == nil {
		t.Fatal("Expected err to be non-nil")
	}
	if err.Error() != "chart metadata (Chart.yaml) missing" {
		t.Errorf("Expected chart metadata missing error, got '%s'", err.Error())
	}

	// legacy check
	c, err = LoadFiles([]*BufferedFile{
		{
			Name: "values.toml",
			Data: []byte{},
		},
	})
	if err == nil {
		t.Fatal("Expected err to be non-nil")
	}
	if err.Error() != "values.toml is illegal as of 2.0.0-alpha.2" {
		t.Errorf("Expected values.toml to be illegal, got '%s'", err.Error())
	}
}

// Packaging the chart on a Windows machine will produce an
// archive that has \\ as delimiters. Test that we support these archives
func TestLoadFileBackslash(t *testing.T) {
	c, err := Load("testdata/frobnitz_backslash-1.2.3.tgz")
	if err != nil {
		t.Fatalf("Failed to load testdata: %s", err)
	}
	verifyChartFileAndTemplate(t, c, "frobnitz_backslash")
	verifyChart(t, c)
	verifyRequirements(t, c)
}

func verifyChart(t *testing.T, c *chart.Chart) {
	if c.Metadata.Name == "" {
		t.Fatalf("No chart metadata found on %v", c)
	}
	t.Logf("Verifying chart %s", c.Metadata.Name)
	if len(c.Templates) != 1 {
		t.Errorf("Expected 1 template, got %d", len(c.Templates))
	}

	numfiles := 8
	if len(c.Files) != numfiles {
		t.Errorf("Expected %d extra files, got %d", numfiles, len(c.Files))
		for _, n := range c.Files {
			t.Logf("\t%s", n.TypeUrl)
		}
	}

	if len(c.Dependencies) != 2 {
		t.Errorf("Expected 2 dependencies, got %d (%v)", len(c.Dependencies), c.Dependencies)
		for _, d := range c.Dependencies {
			t.Logf("\tSubchart: %s\n", d.Metadata.Name)
		}
	}

	expect := map[string]map[string]string{
		"alpine": {
			"version": "0.1.0",
		},
		"mariner": {
			"version": "4.3.2",
		},
	}

	for _, dep := range c.Dependencies {
		if dep.Metadata == nil {
			t.Fatalf("expected metadata on dependency: %v", dep)
		}
		exp, ok := expect[dep.Metadata.Name]
		if !ok {
			t.Fatalf("Unknown dependency %s", dep.Metadata.Name)
		}
		if exp["version"] != dep.Metadata.Version {
			t.Errorf("Expected %s version %s, got %s", dep.Metadata.Name, exp["version"], dep.Metadata.Version)
		}
	}

}

func verifyRequirements(t *testing.T, c *chart.Chart) {
	r, err := LoadRequirements(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Dependencies) != 2 {
		t.Errorf("Expected 2 requirements, got %d", len(r.Dependencies))
	}
	tests := []*Dependency{
		{Name: "alpine", Version: "0.1.0", Repository: "https://example.com/charts"},
		{Name: "mariner", Version: "4.3.2", Repository: "https://example.com/charts"},
	}
	for i, tt := range tests {
		d := r.Dependencies[i]
		if d.Name != tt.Name {
			t.Errorf("Expected dependency named %q, got %q", tt.Name, d.Name)
		}
		if d.Version != tt.Version {
			t.Errorf("Expected dependency named %q to have version %q, got %q", tt.Name, tt.Version, d.Version)
		}
		if d.Repository != tt.Repository {
			t.Errorf("Expected dependency named %q to have repository %q, got %q", tt.Name, tt.Repository, d.Repository)
		}
	}
}
func verifyRequirementsLock(t *testing.T, c *chart.Chart) {
	r, err := LoadRequirementsLock(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Dependencies) != 2 {
		t.Errorf("Expected 2 requirements, got %d", len(r.Dependencies))
	}
	tests := []*Dependency{
		{Name: "alpine", Version: "0.1.0", Repository: "https://example.com/charts"},
		{Name: "mariner", Version: "4.3.2", Repository: "https://example.com/charts"},
	}
	for i, tt := range tests {
		d := r.Dependencies[i]
		if d.Name != tt.Name {
			t.Errorf("Expected dependency named %q, got %q", tt.Name, d.Name)
		}
		if d.Version != tt.Version {
			t.Errorf("Expected dependency named %q to have version %q, got %q", tt.Name, tt.Version, d.Version)
		}
		if d.Repository != tt.Repository {
			t.Errorf("Expected dependency named %q to have repository %q, got %q", tt.Name, tt.Repository, d.Repository)
		}
	}
}

func verifyFrobnitz(t *testing.T, c *chart.Chart) {
	verifyChartFileAndTemplate(t, c, "frobnitz")
}

func verifyChartFileAndTemplate(t *testing.T, c *chart.Chart, name string) {

	verifyChartfile(t, c.Metadata, name)

	if len(c.Templates) != 1 {
		t.Fatalf("Expected 1 template, got %d", len(c.Templates))
	}

	if c.Templates[0].Name != "templates/template.tpl" {
		t.Errorf("Unexpected template: %s", c.Templates[0].Name)
	}

	if len(c.Templates[0].Data) == 0 {
		t.Error("No template data.")
	}
}
