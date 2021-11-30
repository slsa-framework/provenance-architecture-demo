package main

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/google/go-github/v40/github"
	"github.com/in-toto/in-toto-golang/in_toto"
	"google.golang.org/api/cloudbuild/v1"
)

type ReleaseType int

const (
	unknownReleaseType = iota
	// Source Distribution formats (non-exhaustive).
	// See https://docs.python.org/3/distutils/sourcedist.html#creating-a-source-distribution
	sourceZip
	sourceGztar
	// Wheel platform identifiers.
	// https://packaging.python.org/specifications/platform-compatibility-tags/
	// https://www.python.org/dev/peps/pep-0425/#platform-tag
	wheelAny
	wheelManylinux
	wheelMusllinux
	wheelMacos
	wheelWin
)

func getReleaseType(releaseFile string) ReleaseType {
	switch {
	case strings.HasSuffix(releaseFile, ".tar.gz"):
		return sourceGztar
	case strings.HasSuffix(releaseFile, ".zip"):
		return sourceZip
	case strings.HasSuffix(releaseFile, ".whl"):
		segs := strings.Split(strings.TrimSuffix(releaseFile, ".whl"), "-")
		platform := strings.Split(segs[len(segs)-1], ".")[0]
		switch {
		case platform == "any":
			return wheelAny
		case strings.HasPrefix(platform, "manylinux"):
			return wheelManylinux
		case strings.HasPrefix(platform, "musllinux"):
			return wheelMusllinux
		case strings.HasPrefix(platform, "macos"):
			return wheelMacos
		case strings.HasPrefix(platform, "win"):
			return wheelWin
		}
	}
	return unknownReleaseType
}

type RebuilderOptions struct {
	Types       []ReleaseType
	PackageRoot *string
	Version     *string
}

func Rebuild(pkg, repo string, opt RebuilderOptions) (*[]in_toto.ProvenanceStatement, error) {
	proj := pypiMetadata(pkg)
	var version string
	if opt.Version == nil || *opt.Version == "" {
		version = proj.LatestVersion
	} else {
		version = *opt.Version
	}
	// Find release artifacts.
	var toRebuild []Release
	for _, r := range proj.Releases[version] {
		for _, t := range opt.Types {
			// NOTE: Python 2 builds not supported.
			if r.PythonVersion == "py2" {
				continue
			}
			if t == getReleaseType(r.Filename) {
				toRebuild = append(toRebuild, r)
			}
		}
	}
	if len(toRebuild) == 0 {
		return nil, fmt.Errorf("No release to rebuild [pkg=%s, types=%v]", pkg, opt.Types)
	}
	// Find appropriate tag.
	repoRe := regexp.MustCompile("github.com/([^/]*)/([^/]*)")
	groups := repoRe.FindStringSubmatch(repo)
	repoOwner, repoName := groups[1], groups[2]
	re := regexp.MustCompile(fmt.Sprintf(`^(.*[^0-9])?%s([^abdp\-\.].*)?$`, version))
	client := githubClient(*githubToken)
	tags, _, err := client.Repositories.ListTags(context.Background(), repoOwner, repoName, nil)
	if err != nil {
		return nil, err
	}
	var tag string
	for _, t := range tags {
		if re.MatchString(t.GetName()) {
			tag = t.GetName()
			break
		}
	}
	if tag == "" {
		return nil, fmt.Errorf("No tag found [pkg=%s, repo=%s, version=%s]", pkg, repo, version)
	}
	// Validate package root path.
	var packageDir string
	if opt.PackageRoot == nil || *opt.PackageRoot == "" {
		packageDir = "."
	} else {
		packageDir = *opt.PackageRoot
	}
	file, _, _, err := client.Repositories.GetContents(context.Background(), repoOwner, repoName, filepath.Join(packageDir, "setup.py"), &github.RepositoryContentGetOptions{Ref: tag})
	if file == nil {
		return nil, fmt.Errorf("No setup.py file found in package root [pkg=%s, repo=%s, tag=%s, path=%s]", pkg, repo, tag, packageDir)
	}
	// Do rebuilds.
	var stmts []in_toto.ProvenanceStatement
	for _, r := range toRebuild {
		switch getReleaseType(r.Filename) {
		case wheelAny:
			prov, err := rebuildWheel(r, pkg, repo, tag, packageDir)
			if err != nil {
				return nil, err
			}
			stmts = append(stmts, *prov)
		default:
			return nil, fmt.Errorf("Release type not supported [pkg=%s, version=%s, type=%v]", pkg, version, getReleaseType(r.Filename))
		}
	}
	return &stmts, nil
}

func rebuildWheel(wheel Release, pkg, repo, tag, packageRoot string) (*in_toto.ProvenanceStatement, error) {
	start := time.Now()
	origWhl := get(wheel.URL)
	r, err := zip.NewReader(bytes.NewReader(origWhl), int64(len(origWhl)))
	if err != nil {
		log.Fatal(err)
	}
	var metadata, wheelInfo []byte
	python := "python3.9"
	for _, f := range r.File {
		switch {
		case strings.HasSuffix(f.Name, ".dist-info/METADATA"):
			reader, err := f.Open()
			if err != nil {
				log.Fatal(err)
			}
			metadata, err = ioutil.ReadAll(reader)
			if err != nil {
				log.Fatal(err)
			}
		case strings.HasSuffix(f.Name, ".dist-info/WHEEL"):
			reader, err := f.Open()
			if err != nil {
				log.Fatal(err)
			}
			wheelInfo, err = ioutil.ReadAll(reader)
			if err != nil {
				log.Fatal(err)
			}
		case strings.HasSuffix(f.Name, "-nspkg.pth"):
			// Names of the form: "pkg_name-version-py3.10-nspkg.pth"
			pthRe := regexp.MustCompile(`[^-]+-[^-]+-py(\d+\.\d+)-nspkg.pth`)
			segs := pthRe.FindStringSubmatch(f.Name)
			if len(segs) == 0 {
				break
			}
			version := segs[1]
			// XXX: alpine pkg constraint
			if version != "3.9" {
				return nil, errors.New("Unsupported python version")
			}
			python = "python" + version
		}
	}
	if len(metadata) == 0 {
		log.Fatal("No METADATA found")
	}
	deps := make(map[string]string, 2)
	re := regexp.MustCompile(`Generator: bdist_wheel \(([\.\d]*)\)`)
	deps["wheel"] = "==" + string(re.FindSubmatch(wheelInfo)[1])
	switch {
	case bytes.Contains(metadata, []byte("License-File")):
		deps["setuptools"] = "==58.3.0"
	default:
		deps["setuptools"] = "==56.2.0"
	}
	svc, err := cloudbuild.NewService(context.Background())
	op, err := svc.Projects.Builds.Create(*project, &cloudbuild.Build{
		Substitutions: map[string]string{
			"_FILENAME":    wheel.Filename,
			"_URL":         wheel.URL,
			"_REPO":        repo,
			"_TAG":         tag,
			"_SETUPTOOLS":  deps["setuptools"],
			"_WHEEL":       deps["wheel"],
			"_PACKAGEROOT": packageRoot,
		},
		Steps: []*cloudbuild.BuildStep{
			&cloudbuild.BuildStep{
				Name: "gcr.io/cloud-builders/git",
				Args: []string{"clone", "--branch", "${_TAG}", "--single-branch", "https://${_REPO}", "repo"},
			},
			&cloudbuild.BuildStep{
				Name: "gcr.io/cloud-builders/curl",
				Args: []string{"--output", "${_FILENAME}", "${_URL}"},
			},
			&cloudbuild.BuildStep{
				Name:       "alpine",
				Entrypoint: "/bin/sh",
				Args: []string{"-c", `
					apk add python3 py3-pip git &&
    			mkdir env &&
    			python3 -m venv env &&
    			env/bin/pip3 install setuptools${_SETUPTOOLS} wheel${_WHEEL} &&
    			cd repo/${_PACKAGEROOT} &&
    			/workspace/env/bin/python3.9 setup.py build bdist_wheel
			`},
			},
			&cloudbuild.BuildStep{
				Name: "gcr.io/" + *project + "/transfer_metadata",
				Args: []string{"${_FILENAME}", "repo/${_PACKAGEROOT}/dist/${_FILENAME}"},
			},
			&cloudbuild.BuildStep{
				Name:       "alpine",
				Entrypoint: "/bin/sh",
				Args: []string{"-c", `
					apk add python3 py3-pip libmagic libarchive unzip &&
					env/bin/pip3 install diffoscope &&
					env/bin/diffoscope ${_FILENAME} repo/${_PACKAGEROOT}/dist/${_FILENAME}
			`},
			},
		}}).Do()
	if err != nil {
		return nil, err
	}
	for !op.Done {
		time.Sleep(10 * time.Second)
		op, err = svc.Operations.Get(op.Name).Do()
		if err != nil {
			log.Fatal(err)
		}
	}
	end := time.Now()
	if op.Error != nil {
		errTxt, err := op.Error.MarshalJSON()
		if err != nil {
			log.Fatal(err)
		}
		return nil, errors.New(string(errTxt))
	}
	// Construct and return SLSA provenance.
	c := githubClient(*githubToken)
	parts := strings.Split(repo, "/")
	hash, _, err := c.Repositories.GetCommitSHA1(context.Background(), parts[1], parts[2], tag, "")
	if err != nil {
		log.Fatal(err)
	}
	stmt := in_toto.ProvenanceStatement{
		in_toto.StatementHeader{
			Type:          "https://in-toto.io/Statement/v0.1",
			PredicateType: "https://slsa.dev/provenance/v0.1",
			Subject:       []in_toto.Subject{{Name: wheel.Filename, Digest: in_toto.DigestSet{"sha256": wheel.Digests.SHA256}}},
		},
		in_toto.ProvenancePredicate{
			in_toto.ProvenanceBuilder{ID: "https://demo.slsa.dev/rebuilder@v1"},
			in_toto.ProvenanceRecipe{
				Type:       "https://slsa.github.com/workflow@v1",
				EntryPoint: packageRoot + "/setup.py",
				Arguments: []string{
					fmt.Sprintf("git clone --branch=%s --single-branch %s", tag, repo),
					fmt.Sprintf("%s -m venv /tmp/env", python),
					fmt.Sprintf("/tmp/env/bin/pip3 install setuptools%s wheel%s", deps["setuptools"], deps["wheel"]),
					fmt.Sprintf("cd %s", packageRoot),
					fmt.Sprintf("/tmp/env/bin/%s setup.py build bdist_wheel", python),
				},
				Environment: []string{},
			},
			&in_toto.ProvenanceMetadata{
				BuildStartedOn:  &start,
				BuildFinishedOn: &end,
				Completeness:    in_toto.ProvenanceComplete{Arguments: true, Environment: false, Materials: false},
				Reproducible:    false,
			},
			[]in_toto.ProvenanceMaterial{
				{
					URI:    fmt.Sprintf("git+https://%s@%s", repo, tag),
					Digest: in_toto.DigestSet{"sha1": hash},
				},
			},
		},
	}
	return &stmt, nil
}
